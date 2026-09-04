package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v81"
	"github.com/thanhpk/randstr"
)

const (
	stripeSubscriptionCurrency           = "USD"
	stripeSubscriptionCurrencyScale      = int64(100)
	stripeSubscriptionBindingMetadataKey = "new_api_sub_binding"
	stripeSubscriptionBindingTokenLength = 32
)

type SubscriptionStripePayRequest struct {
	PlanId int `json:"plan_id"`
}

func SubscriptionRequestStripePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionStripePayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorMsg(c, "套餐未启用")
		return
	}
	if strings.TrimSpace(plan.StripePriceId) == "" {
		common.ApiErrorMsg(c, "该套餐未配置 StripePriceId")
		return
	}
	if !strings.HasPrefix(setting.StripeApiSecret, "sk_") && !strings.HasPrefix(setting.StripeApiSecret, "rk_") {
		common.ApiErrorMsg(c, "Stripe 未配置或密钥无效")
		return
	}
	if setting.StripeWebhookSecret == "" {
		common.ApiErrorMsg(c, "Stripe Webhook 未配置")
		return
	}

	planCurrency := strings.ToUpper(strings.TrimSpace(plan.Currency))
	if planCurrency != stripeSubscriptionCurrency {
		common.ApiErrorMsg(c, "订阅套餐目前仅支持 USD")
		return
	}
	planAmount := decimal.NewFromFloat(plan.PriceAmount).Round(2)
	if planAmount.LessThanOrEqual(decimal.Zero) {
		common.ApiErrorMsg(c, "订阅套餐价格无效")
		return
	}
	expectedAmountUnit := planAmount.Mul(decimal.NewFromInt(stripeSubscriptionCurrencyScale)).IntPart()
	if expectedAmountUnit <= 0 {
		common.ApiErrorMsg(c, "订阅套餐价格无效")
		return
	}
	entitlementSnapshot, err := model.NewSubscriptionEntitlementSnapshot(plan)
	if err != nil {
		common.ApiErrorMsg(c, "订阅套餐权益配置无效")
		return
	}
	entitlementSnapshotJSON, err := entitlementSnapshot.Marshal()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	userId := c.GetInt("id")
	user, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if user == nil {
		common.ApiErrorMsg(c, "用户不存在")
		return
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorMsg(c, "已达到该套餐购买上限")
			return
		}
	}

	reference := fmt.Sprintf("sub-stripe-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "sub_ref_" + common.Sha1([]byte(reference))
	bindingToken, err := common.GenerateRandomCharsKey(stripeSubscriptionBindingTokenLength)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 生成订阅订单绑定令牌失败 user_id=%d trade_no=%s error=%q", userId, referenceId, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	order := &model.SubscriptionOrder{
		UserId:                     userId,
		PlanId:                     plan.Id,
		Money:                      planAmount.InexactFloat64(),
		PaymentExpectationVersion:  model.StripeSubscriptionPaymentExpectationVersion,
		ExpectedAmount:             planAmount.InexactFloat64(),
		ExpectedAmountUnit:         expectedAmountUnit,
		ExpectedCurrency:           planCurrency,
		ExpectedBindingToken:       bindingToken,
		EntitlementSnapshotVersion: model.SubscriptionEntitlementSnapshotVersion,
		EntitlementSnapshot:        entitlementSnapshotJSON,
		TradeNo:                    referenceId,
		PaymentMethod:              model.PaymentMethodStripe,
		PaymentProvider:            model.PaymentProviderStripe,
		CreateTime:                 time.Now().Unix(),
		Status:                     common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 创建订阅订单失败 user_id=%d plan_id=%d trade_no=%s error=%q", userId, plan.Id, referenceId, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	checkoutSession, err := genStripeSubscriptionSession(referenceId, bindingToken, user.StripeCustomer, user.Email, plan.StripePriceId)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 订阅支付链接创建失败 trade_no=%s plan_id=%d error=%q", referenceId, plan.Id, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if checkoutSession == nil || checkoutSession.ID == "" || checkoutSession.URL == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 订阅 Checkout Session 返回无效 trade_no=%s plan_id=%d", referenceId, plan.Id))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := model.SetStripeSubscriptionOrderExpectedSessionID(referenceId, checkoutSession.ID); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 持久化订阅 Checkout Session 失败 trade_no=%s plan_id=%d session_id=%s error=%q", referenceId, plan.Id, checkoutSession.ID, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Stripe 订阅订单创建成功 user_id=%d plan_id=%d trade_no=%s money=%.2f currency=%s session_id=%s", userId, plan.Id, referenceId, order.Money, order.ExpectedCurrency, checkoutSession.ID))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": checkoutSession.URL,
		},
	})
}

func genStripeSubscriptionSession(referenceId string, bindingToken string, customerId string, email string, priceId string) (*stripe.CheckoutSession, error) {
	if strings.TrimSpace(referenceId) == "" || strings.TrimSpace(bindingToken) == "" || strings.TrimSpace(priceId) == "" {
		return nil, fmt.Errorf("无效的 Stripe 订阅结账参数")
	}
	stripe.Key = setting.StripeApiSecret

	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceId),
		SuccessURL:        stripe.String(paymentReturnPath("/wallet")),
		CancelURL:         stripe.String(paymentReturnPath("/wallet")),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceId),
				Quantity: stripe.Int64(1),
			},
		},
		Mode:                stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		AllowPromotionCodes: stripe.Bool(false),
	}
	params.SetIdempotencyKey(referenceId)
	params.AddMetadata(stripeSubscriptionBindingMetadataKey, bindingToken)

	if customerId == "" {
		if email != "" {
			params.CustomerEmail = stripe.String(email)
		}
		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	} else {
		params.Customer = stripe.String(customerId)
	}

	return createStripeCheckoutSession(params)
}
