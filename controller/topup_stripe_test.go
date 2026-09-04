package controller

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/webhook"
	"gorm.io/gorm"
)

func withStripeWalletQuoteSettings(t *testing.T) {
	t.Helper()

	originalUnitPrice := setting.StripeUnitPrice
	originalFeePercent := operation_setting.GetPaymentSetting().StripeFeePercent
	originalFeeFixed := operation_setting.GetPaymentSetting().StripeFeeFixed
	originalQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalDiscounts := make(map[int]float64, len(operation_setting.GetPaymentSetting().AmountDiscount))
	for amount, discount := range operation_setting.GetPaymentSetting().AmountDiscount {
		originalDiscounts[amount] = discount
	}
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()
	t.Cleanup(func() {
		setting.StripeUnitPrice = originalUnitPrice
		operation_setting.GetPaymentSetting().StripeFeePercent = originalFeePercent
		operation_setting.GetPaymentSetting().StripeFeeFixed = originalFeeFixed
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalQuotaDisplayType
		operation_setting.GetPaymentSetting().AmountDiscount = originalDiscounts
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})
}

func TestGetStripeMinAndMaxTopupUseDisplayUnits(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	previousQuotaPerUnit := common.QuotaPerUnit
	previousMinTopUp := setting.StripeMinTopUp
	t.Cleanup(func() {
		common.QuotaPerUnit = previousQuotaPerUnit
		setting.StripeMinTopUp = previousMinTopUp
	})

	setting.StripeMinTopUp = 2
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	assert.Equal(t, int64(2), getStripeMinTopup())
	assert.Equal(t, int64(10000), getStripeMaxTopup())

	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens
	assert.Equal(t, int64(1_000_000), getStripeMinTopup())
	assert.Equal(t, int64(5_000_000_000), getStripeMaxTopup())
	assert.LessOrEqual(t, getStripeMinTopup(), getStripeMaxTopup())

	setting.StripeMinTopUp = 1
	common.QuotaPerUnit = 1.5
	assert.Equal(t, int64(2), getStripeMinTopup())
	assert.Equal(t, int64(15000), getStripeMaxTopup())
}

func TestGetStripeMinAndMaxTopupRejectInvalidQuotaPerUnit(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	previousQuotaPerUnit := common.QuotaPerUnit
	previousMinTopUp := setting.StripeMinTopUp
	t.Cleanup(func() {
		common.QuotaPerUnit = previousQuotaPerUnit
		setting.StripeMinTopUp = previousMinTopUp
	})

	setting.StripeMinTopUp = 1
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens

	for _, quotaPerUnit := range []float64{
		0,
		-1,
		math.NaN(),
		math.Inf(1),
		math.Inf(-1),
		float64(math.MaxInt64),
	} {
		common.QuotaPerUnit = quotaPerUnit
		_, _, err := getStripeTopupBounds()
		require.Error(t, err)
	}
}

func TestStripeRequestAmountFailsClosedForInvalidQuotaPerUnit(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	previousQuotaPerUnit := common.QuotaPerUnit
	previousMinTopUp := setting.StripeMinTopUp
	common.QuotaPerUnit = 0
	setting.StripeMinTopUp = 1
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens
	t.Cleanup(func() {
		common.QuotaPerUnit = previousQuotaPerUnit
		setting.StripeMinTopUp = previousMinTopUp
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	stripeAdaptor.RequestAmount(context, &StripePayRequest{Amount: 1})

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"message":"error"`)
	assert.Contains(t, recorder.Body.String(), "Stripe 充值配置无效")
}

func TestStripeTokensMinimumProducesValidQuote(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	previousQuotaPerUnit := common.QuotaPerUnit
	previousMinTopUp := setting.StripeMinTopUp
	common.QuotaPerUnit = 500000
	setting.StripeMinTopUp = 1
	setting.StripeUnitPrice = 1
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1}`))
	t.Cleanup(func() {
		common.QuotaPerUnit = previousQuotaPerUnit
		setting.StripeMinTopUp = previousMinTopUp
	})

	amount := getStripeMinTopup()
	assert.LessOrEqual(t, amount, getStripeMaxTopup())

	creditedQuota, err := validateCreditedQuota(getStripeCreditedQuota(amount, "default"))
	require.NoError(t, err)
	assert.Equal(t, 500000, creditedQuota)

	quote, ok := getStripeWalletQuote(amount, "default")
	require.True(t, ok)
	assert.Equal(t, "1.00", quote.Amount.StringFixed(2))
	assert.Equal(t, int64(100), quote.AmountUnit)
	assert.Equal(t, stripeWalletCurrency, quote.Currency)
}

func TestGetTopUpInfoUsesEffectiveStripeMinimumWithoutMutatingSettings(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	previousPayMethods := operation_setting.PayMethods
	previousCompliance := *operation_setting.GetPaymentSetting()
	previousAPISecret := setting.StripeApiSecret
	previousWebhookSecret := setting.StripeWebhookSecret
	previousMinTopUp := setting.StripeMinTopUp
	t.Cleanup(func() {
		operation_setting.PayMethods = previousPayMethods
		*operation_setting.GetPaymentSetting() = previousCompliance
		setting.StripeApiSecret = previousAPISecret
		setting.StripeWebhookSecret = previousWebhookSecret
		setting.StripeMinTopUp = previousMinTopUp
	})

	operation_setting.PayMethods = []map[string]string{{
		"name":      "Stripe",
		"type":      model.PaymentMethodStripe,
		"min_topup": "2",
	}}
	operation_setting.GetPaymentSetting().ComplianceConfirmed = true
	operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens
	setting.StripeApiSecret = "configured"
	setting.StripeWebhookSecret = "configured"
	setting.StripeMinTopUp = 2

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	GetTopUpInfo(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			StripeMinTopup int64               `json:"stripe_min_topup"`
			PayMethods     []map[string]string `json:"pay_methods"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	expectedMinimum := int64(2 * common.QuotaPerUnit)
	assert.Equal(t, expectedMinimum, response.Data.StripeMinTopup)
	require.Len(t, response.Data.PayMethods, 1)
	assert.Equal(t, fmt.Sprint(expectedMinimum), response.Data.PayMethods[0]["min_topup"])
	assert.Equal(t, "2", operation_setting.PayMethods[0]["min_topup"])
}

func TestGetStripeWalletQuote(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	setting.StripeUnitPrice = 2.5
	operation_setting.GetPaymentSetting().StripeFeePercent = 0
	operation_setting.GetPaymentSetting().StripeFeeFixed = 0
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{
		10:                           0.8,
		int(common.QuotaPerUnit * 3): 0.5,
		20:                           0,
	}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"vip":1.2}`))

	testCases := []struct {
		name             string
		amount           int64
		group            string
		quotaDisplayType string
		expectedAmount   string
		expectedUnit     int64
	}{
		{
			name:             "currency display applies unit price group ratio and discount",
			amount:           10,
			group:            "vip",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expectedAmount:   "24.00",
			expectedUnit:     2400,
		},
		{
			name:             "tokens display converts quota before pricing",
			amount:           int64(common.QuotaPerUnit * 3),
			group:            "vip",
			quotaDisplayType: operation_setting.QuotaDisplayTypeTokens,
			expectedAmount:   "4.50",
			expectedUnit:     450,
		},
		{
			name:             "non positive discount falls back to no discount",
			amount:           20,
			group:            "default",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expectedAmount:   "50.00",
			expectedUnit:     5000,
		},
		{
			name:             "normalizes half cents before deriving minor units",
			amount:           1,
			group:            "default",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expectedAmount:   "2.50",
			expectedUnit:     250,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = tc.quotaDisplayType
			quote, ok := getStripeWalletQuote(tc.amount, tc.group)
			require.True(t, ok)

			assert.Equal(t, tc.expectedAmount, quote.Amount.StringFixed(2))
			assert.Equal(t, tc.expectedUnit, quote.AmountUnit)
			assert.Equal(t, stripeWalletCurrency, quote.Currency)
			assert.Equal(t, quote.AmountUnit, quote.Amount.Mul(decimal.NewFromInt(stripeWalletCurrencyScale)).IntPart())
		})
	}
}

func TestGetStripeWalletQuoteAppliesFeesWithoutChangingCreditedQuota(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	setting.StripeUnitPrice = 2
	operation_setting.GetPaymentSetting().StripeFeePercent = 5
	operation_setting.GetPaymentSetting().StripeFeeFixed = 0.5
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{10: 0.8}
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"vip":1.25}`))

	creditedBefore := getStripeCreditedQuota(10, "vip")
	quote, ok := getStripeWalletQuote(10, "vip")
	require.True(t, ok)

	assert.Equal(t, "21.50", quote.Amount.StringFixed(2))
	assert.Equal(t, int64(2150), quote.AmountUnit)
	assert.True(t, creditedBefore.Equal(decimal.NewFromFloat(12.5).Mul(decimal.NewFromFloat(common.QuotaPerUnit))))
}

func TestGetStripeWalletQuoteRejectsInvalidFees(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	setting.StripeUnitPrice = 1
	operation_setting.GetPaymentSetting().StripeFeePercent = math.NaN()
	operation_setting.GetPaymentSetting().StripeFeeFixed = 0

	quote, ok := getStripeWalletQuote(10, "default")
	assert.False(t, ok)
	assert.True(t, quote.Amount.IsZero())
	assert.Equal(t, stripeWalletCurrency, quote.Currency)
}

func TestGenStripeCheckoutSession_UsesNormalizedDynamicPrice(t *testing.T) {
	originalAPISecret := setting.StripeApiSecret
	originalCreateSession := createStripeCheckoutSession
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		createStripeCheckoutSession = originalCreateSession
	})
	setting.StripeApiSecret = "sk_test_wallet_quote"

	var captured *stripe.CheckoutSessionParams
	createStripeCheckoutSession = func(params *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
		captured = params
		return &stripe.CheckoutSession{ID: "cs_test", URL: "https://checkout.example.test/session"}, nil
	}

	quote := stripeWalletQuote{
		Amount:     decimal.RequireFromString("12.34"),
		AmountUnit: 1234,
		Currency:   stripeWalletCurrency,
	}
	checkoutSession, err := genStripeCheckoutSession(
		"ref_wallet_quote",
		"binding_token_wallet_quote",
		"",
		"user@example.com",
		quote,
		"https://example.com/success",
		"https://example.com/cancel",
	)
	require.NoError(t, err)
	require.NotNil(t, checkoutSession)
	require.NotNil(t, captured)
	require.Len(t, captured.LineItems, 1)

	lineItem := captured.LineItems[0]
	assert.Nil(t, lineItem.Price)
	require.NotNil(t, lineItem.PriceData)
	assert.Equal(t, "usd", stripe.StringValue(lineItem.PriceData.Currency))
	assert.Equal(t, int64(1234), stripe.Int64Value(lineItem.PriceData.UnitAmount))
	assert.Equal(t, int64(1), stripe.Int64Value(lineItem.Quantity))
	require.NotNil(t, lineItem.PriceData.ProductData)
	assert.Equal(t, stripeWalletProductName, stripe.StringValue(lineItem.PriceData.ProductData.Name))
	assert.Equal(t, "ref_wallet_quote", stripe.StringValue(captured.ClientReferenceID))
	assert.Equal(t, "binding_token_wallet_quote", captured.Metadata[stripeWalletBindingMetadataKey])
	assert.Equal(t, "user@example.com", stripe.StringValue(captured.CustomerEmail))
	assert.Equal(t, string(stripe.CheckoutSessionCustomerCreationAlways), stripe.StringValue(captured.CustomerCreation))
	assert.Empty(t, stripe.StringValue(captured.Customer))
	assert.Equal(t, "ref_wallet_quote", stripe.StringValue(captured.Params.IdempotencyKey))
	assert.False(t, stripe.BoolValue(captured.AllowPromotionCodes))
}

func TestGenStripeCheckoutSession_UsesExistingCustomerAndRejectsInvalidQuote(t *testing.T) {
	originalAPISecret := setting.StripeApiSecret
	originalCreateSession := createStripeCheckoutSession
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		createStripeCheckoutSession = originalCreateSession
	})
	setting.StripeApiSecret = "sk_test_wallet_quote"

	called := false
	var captured *stripe.CheckoutSessionParams
	createStripeCheckoutSession = func(params *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
		called = true
		captured = params
		return &stripe.CheckoutSession{ID: "cs_test", URL: "https://checkout.example.test/session"}, nil
	}

	validQuote := stripeWalletQuote{
		Amount:     decimal.RequireFromString("1.00"),
		AmountUnit: 100,
		Currency:   stripeWalletCurrency,
	}
	_, err := genStripeCheckoutSession("ref_existing_customer", "binding_token_existing_customer", "cus_123", "ignored@example.com", validQuote, "", "")
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, captured)
	assert.Equal(t, "cus_123", stripe.StringValue(captured.Customer))
	assert.Empty(t, stripe.StringValue(captured.CustomerEmail))
	assert.Empty(t, stripe.StringValue(captured.CustomerCreation))

	called = false
	_, err = genStripeCheckoutSession("ref_invalid_quote", "binding_token_invalid_quote", "", "", stripeWalletQuote{Currency: "CNY"}, "", "")
	require.Error(t, err)
	assert.False(t, called)

	_, err = genStripeCheckoutSession("ref_missing_binding", "", "", "", validQuote, "", "")
	require.Error(t, err)
	assert.False(t, called)
}

func setupStripeWebhookTest(t *testing.T) *gorm.DB {
	t.Helper()

	// Stripe controller fixtures use fresh SQLite databases. Invalidate the
	// process-wide plan cache so a previous fixture cannot leak plan ID 1.
	model.InvalidateSubscriptionPlanCache(1)
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	previousAPISecret := setting.StripeApiSecret
	previousWebhookSecret := setting.StripeWebhookSecret

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.TopUp{},
		&model.Log{},
		&model.SubscriptionPlan{},
		&model.SubscriptionOrder{},
		&model.UserSubscription{},
	))
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	setting.StripeApiSecret = "sk_test_webhook"
	setting.StripeWebhookSecret = "whsec_test_webhook"
	confirmPaymentComplianceForTest(t)

	t.Cleanup(func() {
		model.InvalidateSubscriptionPlanCache(1)
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.RedisEnabled = previousRedisEnabled
		setting.StripeApiSecret = previousAPISecret
		setting.StripeWebhookSecret = previousWebhookSecret
	})
	return db
}

func signedStripeWebhookRequest(t *testing.T, payload []byte) *http.Request {
	t.Helper()
	timestamp := time.Now()
	signature := webhook.ComputeSignature(timestamp, payload, setting.StripeWebhookSecret)
	header := fmt.Sprintf("t=%d,v1=%s", timestamp.Unix(), hex.EncodeToString(signature))
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", bytes.NewReader(payload))
	request.Header.Set("Stripe-Signature", header)
	return request
}

func stripeCheckoutSessionPayloadWithType(eventType string, tradeNo string, sessionID string, metadataKey string, bindingToken string, amountUnit int64, currency string, paymentStatus string) []byte {
	return []byte(fmt.Sprintf(`{
		"id":"evt_test",
		"object":"event",
		"type":%q,
		"data":{"object":{
			"id":%q,
			"object":"checkout.session",
			"client_reference_id":%q,
			"customer":"cus_test",
			"metadata":{%q:%q},
			"status":"complete",
			"payment_status":%q,
			"amount_total":%d,
			"currency":%q
		}}
	}`, eventType, sessionID, tradeNo, metadataKey, bindingToken, paymentStatus, amountUnit, currency))
}

func stripeCheckoutCompletedPayloadWithMetadataKey(tradeNo string, sessionID string, metadataKey string, bindingToken string, amountUnit int64, currency string, paymentStatus string) []byte {
	return stripeCheckoutSessionPayloadWithType(
		string(stripe.EventTypeCheckoutSessionCompleted),
		tradeNo,
		sessionID,
		metadataKey,
		bindingToken,
		amountUnit,
		currency,
		paymentStatus,
	)
}

func stripeCheckoutCompletedPayload(tradeNo string, sessionID string, bindingToken string, amountUnit int64, currency string, paymentStatus string) []byte {
	return stripeCheckoutCompletedPayloadWithMetadataKey(
		tradeNo,
		sessionID,
		stripeWalletBindingMetadataKey,
		bindingToken,
		amountUnit,
		currency,
		paymentStatus,
	)
}

func invokeStripeWebhook(request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	StripeWebhook(context)
	return recorder
}

const stripeWebhookBindingToken = "test_stripe_webhook_binding_token"

func insertStripeWebhookTopUp(t *testing.T, db *gorm.DB, tradeNo string, sessionID string) int {
	t.Helper()
	const userID = 601
	user := &model.User{
		Id:       userID,
		Username: "stripe-webhook-user",
		Password: "unused",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(user).Error)
	topUp := &model.TopUp{
		UserId:                    userID,
		Amount:                    2,
		Money:                     2,
		TradeNo:                   tradeNo,
		PaymentMethod:             model.PaymentMethodStripe,
		PaymentProvider:           model.PaymentProviderStripe,
		PaymentExpectationVersion: model.StripePaymentExpectationVersion,
		ExpectedAmount:            2,
		ExpectedAmountUnit:        200,
		ExpectedCurrency:          "USD",
		ExpectedCreditedQuota:     int64(2 * common.QuotaPerUnit),
		ExpectedSessionID:         sessionID,
		ExpectedBindingToken:      stripeWebhookBindingToken,
		Status:                    common.TopUpStatusPending,
		CreateTime:                time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())
	return userID
}

func getStripeWebhookUserQuota(t *testing.T, db *gorm.DB, userID int) int64 {
	t.Helper()
	var user model.User
	require.NoError(t, db.Select("quota").Where("id = ?", userID).First(&user).Error)
	return user.Quota
}

func TestStripeWebhook_MatchingBindingRecoversSessionAndSettlesExactlyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupStripeWebhookTest(t)
	const tradeNo = "stripe-webhook-binding-recovery"
	const sessionID = "cs_webhook_binding_recovery"
	userID := insertStripeWebhookTopUp(t, db, tradeNo, "")
	payload := stripeCheckoutCompletedPayload(tradeNo, sessionID, stripeWebhookBindingToken, 200, "usd", "paid")

	first := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, first.Code)
	success := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, success)
	assert.Equal(t, common.TopUpStatusSuccess, success.Status)
	assert.Equal(t, sessionID, success.ExpectedSessionID)
	expectedQuota := int64(2 * common.QuotaPerUnit)
	assert.Equal(t, expectedQuota, getStripeWebhookUserQuota(t, db, userID))

	replay := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, replay.Code)
	assert.Equal(t, expectedQuota, getStripeWebhookUserQuota(t, db, userID))

	differentSessionPayload := stripeCheckoutCompletedPayload(tradeNo, "cs_webhook_binding_other", stripeWebhookBindingToken, 200, "usd", "paid")
	differentSession := invokeStripeWebhook(signedStripeWebhookRequest(t, differentSessionPayload))
	assert.Equal(t, http.StatusOK, differentSession.Code)
	stored := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, sessionID, stored.ExpectedSessionID)
	assert.Equal(t, expectedQuota, getStripeWebhookUserQuota(t, db, userID))
}

func TestStripeWebhook_MissingBindingMetadataIsAcknowledgedWithoutCredit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupStripeWebhookTest(t)
	const tradeNo = "stripe-webhook-missing-binding-metadata"
	userID := insertStripeWebhookTopUp(t, db, tradeNo, "")
	payload := stripeCheckoutCompletedPayload(tradeNo, "cs_missing_binding_metadata", stripeWebhookBindingToken, 200, "usd", "paid")
	metadata := []byte(fmt.Sprintf(`"metadata":{%q:%q}`, stripeWalletBindingMetadataKey, stripeWebhookBindingToken))
	payload = bytes.Replace(payload, metadata, []byte(`"metadata":{}`), 1)
	require.NotContains(t, string(payload), stripeWalletBindingMetadataKey)

	response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, response.Code)
	pending := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, pending)
	assert.Equal(t, common.TopUpStatusPending, pending.Status)
	assert.Empty(t, pending.ExpectedSessionID)
	assert.Zero(t, getStripeWebhookUserQuota(t, db, userID))
}

func TestStripeWebhook_BindingFailuresAreAcknowledgedWithoutCredit(t *testing.T) {
	testCases := []struct {
		name         string
		bindingToken string
	}{
		{name: "empty binding metadata", bindingToken: ""},
		{name: "mismatched binding metadata", bindingToken: "different_binding_token"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := setupStripeWebhookTest(t)
			tradeNo := "stripe-webhook-binding-failure-" + tc.name
			userID := insertStripeWebhookTopUp(t, db, tradeNo, "")
			payload := stripeCheckoutCompletedPayload(tradeNo, "cs_binding_failure", tc.bindingToken, 200, "usd", "paid")

			response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
			assert.Equal(t, http.StatusOK, response.Code)
			pending := model.GetTopUpByTradeNo(tradeNo)
			require.NotNil(t, pending)
			assert.Equal(t, common.TopUpStatusPending, pending.Status)
			assert.Empty(t, pending.ExpectedSessionID)
			assert.Zero(t, getStripeWebhookUserQuota(t, db, userID))
		})
	}
}

func TestStripeWebhook_PermanentMismatchIsAcknowledgedWithoutCredit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupStripeWebhookTest(t)
	const tradeNo = "stripe-webhook-permanent-mismatch"
	userID := insertStripeWebhookTopUp(t, db, tradeNo, "cs_expected")
	payload := stripeCheckoutCompletedPayload(tradeNo, "cs_other", stripeWebhookBindingToken, 200, "usd", "paid")

	response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, response.Code)
	pending := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, pending)
	assert.Equal(t, common.TopUpStatusPending, pending.Status)
	assert.Zero(t, getStripeWebhookUserQuota(t, db, userID))
}

func TestStripeWebhook_UnpaidCompletedSessionIsAcknowledgedWithoutCredit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupStripeWebhookTest(t)
	const tradeNo = "stripe-webhook-unpaid"
	userID := insertStripeWebhookTopUp(t, db, tradeNo, "cs_unpaid")
	payload := stripeCheckoutCompletedPayload(tradeNo, "cs_unpaid", stripeWebhookBindingToken, 200, "usd", "unpaid")

	response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(tradeNo).Status)
	assert.Zero(t, getStripeWebhookUserQuota(t, db, userID))
}

func TestStripeWebhook_MissingLocalOrderIsRetryable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupStripeWebhookTest(t)
	payload := stripeCheckoutCompletedPayload("stripe-webhook-missing-order", "cs_missing_order", stripeWebhookBindingToken, 200, "usd", "paid")

	response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestStripeWebhook_UnsupportedEventIsAcknowledged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupStripeWebhookTest(t)
	payload := []byte(`{"id":"evt_unsupported","object":"event","type":"customer.created","data":{"object":{"id":"cus_test","object":"customer"}}}`)

	response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, response.Code)
}

func TestStripeWebhook_InvalidSignatureIsRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupStripeWebhookTest(t)
	payload := stripeCheckoutCompletedPayload("stripe-invalid-signature", "cs_invalid", stripeWebhookBindingToken, 200, "usd", "paid")
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", bytes.NewReader(payload))
	request.Header.Set("Stripe-Signature", "t=1,v1=invalid")

	response := invokeStripeWebhook(request)
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

func TestStripeWebhook_DisabledWebhookIsRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupStripeWebhookTest(t)
	setting.StripeWebhookSecret = ""
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", bytes.NewReader([]byte(`{}`)))

	response := invokeStripeWebhook(request)
	assert.Equal(t, http.StatusForbidden, response.Code)
}

func TestIsRetryableStripeWebhookError(t *testing.T) {
	testCases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{name: "session binding pending", err: model.ErrStripeSessionBindingPending, retryable: true},
		{name: "wrapped session binding pending", err: fmt.Errorf("settlement: %w", model.ErrStripeSessionBindingPending), retryable: true},
		{name: "topup not found", err: model.ErrTopUpNotFound, retryable: true},
		{name: "subscription order not found", err: model.ErrSubscriptionOrderNotFound, retryable: true},
		{name: "unknown database error", err: gorm.ErrInvalidTransaction, retryable: true},
		{name: "legacy expectation", err: model.ErrLegacyPaymentExpectation, retryable: false},
		{name: "invalid expectation", err: model.ErrPaymentExpectationInvalid, retryable: false},
		{name: "settlement mismatch", err: model.ErrPaymentSettlementMismatch, retryable: false},
		{name: "provider mismatch", err: model.ErrPaymentMethodMismatch, retryable: false},
		{name: "topup status invalid", err: model.ErrTopUpStatusInvalid, retryable: false},
		{name: "subscription status invalid", err: model.ErrSubscriptionOrderStatusInvalid, retryable: false},
		{name: "entitlement invalid", err: model.ErrSubscriptionEntitlementInvalid, retryable: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.retryable, isRetryableStripeWebhookError(tc.err))
		})
	}
}

func TestStripeSubscriptionRequest_PersistsImmutableExpectationBeforeCheckout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupStripeWebhookTest(t)
	previousCreateSession := createStripeCheckoutSession
	t.Cleanup(func() { createStripeCheckoutSession = previousCreateSession })

	allowOverflow := false
	plan := &model.SubscriptionPlan{
		Title:                   "Stripe Checkout Plan",
		PriceAmount:             9.99,
		Currency:                "USD",
		DurationUnit:            model.SubscriptionDurationCustom,
		CustomSeconds:           7200,
		Enabled:                 true,
		StripePriceId:           "price_subscription_test",
		TotalAmount:             2345,
		QuotaResetPeriod:        model.SubscriptionResetCustom,
		QuotaResetCustomSeconds: 600,
		UpgradeGroup:            "vip",
		DowngradeGroup:          "default",
		AllowWalletOverflow:     &allowOverflow,
		MaxPurchasePerUser:      3,
	}
	require.NoError(t, db.Create(plan).Error)
	user := &model.User{
		Id:       701,
		Username: "stripe-subscription-checkout-user",
		Password: "unused",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Email:    "subscription@example.invalid",
	}
	require.NoError(t, db.Create(user).Error)

	var pendingBeforeCheckout model.SubscriptionOrder
	var checkoutParams *stripe.CheckoutSessionParams
	createStripeCheckoutSession = func(params *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
		checkoutParams = params
		require.NoError(t, db.Where("user_id = ?", user.Id).First(&pendingBeforeCheckout).Error)
		return &stripe.CheckoutSession{ID: "cs_subscription_checkout", URL: "https://checkout.example.test/subscription"}, nil
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", user.Id)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/subscription/stripe/pay", bytes.NewReader([]byte(fmt.Sprintf(`{"plan_id":%d}`, plan.Id))))
	context.Request.Header.Set("Content-Type", "application/json")
	SubscriptionRequestStripePay(context)

	require.NotNil(t, checkoutParams)
	assert.Equal(t, common.TopUpStatusPending, pendingBeforeCheckout.Status)
	assert.Equal(t, model.StripeSubscriptionPaymentExpectationVersion, pendingBeforeCheckout.PaymentExpectationVersion)
	assert.InDelta(t, 9.99, pendingBeforeCheckout.ExpectedAmount, 0.000001)
	assert.Equal(t, int64(999), pendingBeforeCheckout.ExpectedAmountUnit)
	assert.Equal(t, "USD", pendingBeforeCheckout.ExpectedCurrency)
	assert.Empty(t, pendingBeforeCheckout.ExpectedSessionID)
	require.Len(t, pendingBeforeCheckout.ExpectedBindingToken, stripeSubscriptionBindingTokenLength)
	assert.Equal(t, model.SubscriptionEntitlementSnapshotVersion, pendingBeforeCheckout.EntitlementSnapshotVersion)
	assert.NotEmpty(t, pendingBeforeCheckout.EntitlementSnapshot)
	assert.Equal(t, pendingBeforeCheckout.TradeNo, stripe.StringValue(checkoutParams.ClientReferenceID))
	assert.Equal(t, pendingBeforeCheckout.TradeNo, stripe.StringValue(checkoutParams.Params.IdempotencyKey))
	assert.Equal(t, pendingBeforeCheckout.ExpectedBindingToken, checkoutParams.Metadata[stripeSubscriptionBindingMetadataKey])
	assert.Equal(t, string(stripe.CheckoutSessionModeSubscription), stripe.StringValue(checkoutParams.Mode))
	assert.False(t, stripe.BoolValue(checkoutParams.AllowPromotionCodes))
	require.Len(t, checkoutParams.LineItems, 1)
	assert.Equal(t, plan.StripePriceId, stripe.StringValue(checkoutParams.LineItems[0].Price))
	assert.Equal(t, int64(1), stripe.Int64Value(checkoutParams.LineItems[0].Quantity))
	assert.Equal(t, user.Email, stripe.StringValue(checkoutParams.CustomerEmail))
	assert.Equal(t, string(stripe.CheckoutSessionCustomerCreationAlways), stripe.StringValue(checkoutParams.CustomerCreation))

	persisted := model.GetSubscriptionOrderByTradeNo(pendingBeforeCheckout.TradeNo)
	require.NotNil(t, persisted)
	assert.Equal(t, "cs_subscription_checkout", persisted.ExpectedSessionID)
	assert.Equal(t, pendingBeforeCheckout.ExpectedBindingToken, persisted.ExpectedBindingToken)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Message string `json:"message"`
		Data    struct {
			PayLink string `json:"pay_link"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "success", response.Message)
	assert.Equal(t, "https://checkout.example.test/subscription", response.Data.PayLink)
	assert.NotContains(t, recorder.Body.String(), pendingBeforeCheckout.ExpectedBindingToken)
}

func TestStripeSubscriptionRequest_RejectsInvalidCheckoutSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupStripeWebhookTest(t)
	previousCreateSession := createStripeCheckoutSession
	t.Cleanup(func() { createStripeCheckoutSession = previousCreateSession })

	plan := &model.SubscriptionPlan{
		Title:            "Stripe Invalid Session Plan",
		PriceAmount:      9.99,
		Currency:         "USD",
		DurationUnit:     model.SubscriptionDurationMonth,
		DurationValue:    1,
		Enabled:          true,
		StripePriceId:    "price_invalid_session",
		TotalAmount:      100,
		QuotaResetPeriod: model.SubscriptionResetNever,
	}
	require.NoError(t, db.Create(plan).Error)
	user := &model.User{
		Id:       703,
		Username: "stripe-invalid-session-user",
		Password: "unused",
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(user).Error)
	createStripeCheckoutSession = func(*stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
		return &stripe.CheckoutSession{URL: "https://checkout.example.test/missing-id"}, nil
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", user.Id)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/subscription/stripe/pay", bytes.NewReader([]byte(fmt.Sprintf(`{"plan_id":%d}`, plan.Id))))
	context.Request.Header.Set("Content-Type", "application/json")
	SubscriptionRequestStripePay(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"message":"error"`)
	var order model.SubscriptionOrder
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&order).Error)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	assert.Empty(t, order.ExpectedSessionID)
	assert.NotEmpty(t, order.ExpectedBindingToken)
	assert.Zero(t, func() int64 {
		var count int64
		require.NoError(t, db.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&count).Error)
		return count
	}())
}

func insertStripeWebhookSubscriptionOrder(t *testing.T, db *gorm.DB, tradeNo string, sessionID string) int {
	t.Helper()
	const userID = 702
	user := &model.User{
		Id:       userID,
		Username: "stripe-subscription-webhook-user",
		Password: "unused",
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(user).Error)
	allowOverflow := false
	plan := &model.SubscriptionPlan{
		Title:                   "Stripe Purchased Plan",
		PriceAmount:             9.99,
		Currency:                "USD",
		DurationUnit:            model.SubscriptionDurationCustom,
		CustomSeconds:           7200,
		Enabled:                 true,
		StripePriceId:           "price_subscription_webhook",
		TotalAmount:             2345,
		QuotaResetPeriod:        model.SubscriptionResetCustom,
		QuotaResetCustomSeconds: 600,
		UpgradeGroup:            "vip",
		DowngradeGroup:          "default",
		AllowWalletOverflow:     &allowOverflow,
		MaxPurchasePerUser:      3,
	}
	require.NoError(t, db.Create(plan).Error)
	snapshot, err := model.NewSubscriptionEntitlementSnapshot(plan)
	require.NoError(t, err)
	snapshotJSON, err := snapshot.Marshal()
	require.NoError(t, err)
	order := &model.SubscriptionOrder{
		UserId:                     userID,
		PlanId:                     plan.Id,
		Money:                      9.99,
		PaymentExpectationVersion:  model.StripeSubscriptionPaymentExpectationVersion,
		ExpectedAmount:             9.99,
		ExpectedAmountUnit:         999,
		ExpectedCurrency:           "USD",
		ExpectedSessionID:          sessionID,
		ExpectedBindingToken:       stripeWebhookBindingToken,
		EntitlementSnapshotVersion: model.SubscriptionEntitlementSnapshotVersion,
		EntitlementSnapshot:        snapshotJSON,
		TradeNo:                    tradeNo,
		PaymentMethod:              model.PaymentMethodStripe,
		PaymentProvider:            model.PaymentProviderStripe,
		Status:                     common.TopUpStatusPending,
		CreateTime:                 time.Now().Unix(),
	}
	require.NoError(t, order.Insert())

	plan.Title = "Mutated Plan"
	plan.CustomSeconds = 60
	plan.TotalAmount = 1
	plan.QuotaResetPeriod = model.SubscriptionResetNever
	plan.QuotaResetCustomSeconds = 0
	plan.UpgradeGroup = ""
	plan.DowngradeGroup = ""
	allowOverflow = true
	plan.AllowWalletOverflow = &allowOverflow
	require.NoError(t, db.Save(plan).Error)
	return userID
}

func TestStripeWebhook_SubscriptionMatchingSettlementUsesSnapshotExactlyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupStripeWebhookTest(t)
	const tradeNo = "stripe-subscription-webhook-match"
	const sessionID = "cs_subscription_webhook"
	userID := insertStripeWebhookSubscriptionOrder(t, db, tradeNo, "")
	payload := stripeCheckoutCompletedPayloadWithMetadataKey(
		tradeNo,
		sessionID,
		stripeSubscriptionBindingMetadataKey,
		stripeWebhookBindingToken,
		999,
		"usd",
		"paid",
	)

	response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, response.Code)
	stored := model.GetSubscriptionOrderByTradeNo(tradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
	assert.Equal(t, sessionID, stored.ExpectedSessionID)
	var subscriptions []model.UserSubscription
	require.NoError(t, db.Where("user_id = ?", userID).Find(&subscriptions).Error)
	require.Len(t, subscriptions, 1)
	assert.Equal(t, int64(2345), subscriptions[0].AmountTotal)
	assert.Equal(t, int64(7200), subscriptions[0].EndTime-subscriptions[0].StartTime)
	assert.Equal(t, int64(600), subscriptions[0].NextResetTime-subscriptions[0].StartTime)
	assert.Equal(t, "vip", subscriptions[0].UpgradeGroup)
	assert.Equal(t, "default", subscriptions[0].DowngradeGroup)
	assert.False(t, subscriptions[0].AllowWalletOverflow)

	replay := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, replay.Code)
	require.NoError(t, db.Where("user_id = ?", userID).Find(&subscriptions).Error)
	assert.Len(t, subscriptions, 1)
}

func TestStripeWebhook_SubscriptionAsyncPaymentEvents(t *testing.T) {
	t.Run("successful delayed payment settles", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := setupStripeWebhookTest(t)
		const tradeNo = "stripe-subscription-webhook-async-success"
		const sessionID = "cs_subscription_async_success"
		userID := insertStripeWebhookSubscriptionOrder(t, db, tradeNo, sessionID)
		payload := stripeCheckoutSessionPayloadWithType(
			string(stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded),
			tradeNo,
			sessionID,
			stripeSubscriptionBindingMetadataKey,
			stripeWebhookBindingToken,
			999,
			"USD",
			"paid",
		)

		response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
		assert.Equal(t, http.StatusOK, response.Code)
		stored := model.GetSubscriptionOrderByTradeNo(tradeNo)
		require.NotNil(t, stored)
		assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
		var count int64
		require.NoError(t, db.Model(&model.UserSubscription{}).Where("user_id = ?", userID).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	})

	t.Run("unpaid delayed success is acknowledged without entitlement", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := setupStripeWebhookTest(t)
		const tradeNo = "stripe-subscription-webhook-async-unpaid"
		const sessionID = "cs_subscription_async_unpaid"
		userID := insertStripeWebhookSubscriptionOrder(t, db, tradeNo, sessionID)
		payload := stripeCheckoutSessionPayloadWithType(
			string(stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded),
			tradeNo,
			sessionID,
			stripeSubscriptionBindingMetadataKey,
			stripeWebhookBindingToken,
			999,
			"USD",
			"unpaid",
		)

		response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
		assert.Equal(t, http.StatusOK, response.Code)
		stored := model.GetSubscriptionOrderByTradeNo(tradeNo)
		require.NotNil(t, stored)
		assert.Equal(t, common.TopUpStatusPending, stored.Status)
		var count int64
		require.NoError(t, db.Model(&model.UserSubscription{}).Where("user_id = ?", userID).Count(&count).Error)
		assert.Zero(t, count)
	})

	t.Run("failed delayed payment closes pending subscription order", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := setupStripeWebhookTest(t)
		const tradeNo = "stripe-subscription-webhook-async-failed"
		const sessionID = "cs_subscription_async_failed"
		userID := insertStripeWebhookSubscriptionOrder(t, db, tradeNo, sessionID)
		payload := stripeCheckoutSessionPayloadWithType(
			string(stripe.EventTypeCheckoutSessionAsyncPaymentFailed),
			tradeNo,
			sessionID,
			stripeSubscriptionBindingMetadataKey,
			stripeWebhookBindingToken,
			999,
			"USD",
			"unpaid",
		)

		response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
		assert.Equal(t, http.StatusOK, response.Code)
		stored := model.GetSubscriptionOrderByTradeNo(tradeNo)
		require.NotNil(t, stored)
		assert.Equal(t, common.TopUpStatusExpired, stored.Status)
		var count int64
		require.NoError(t, db.Model(&model.UserSubscription{}).Where("user_id = ?", userID).Count(&count).Error)
		assert.Zero(t, count)
	})
}

func TestStripeWebhook_SubscriptionMismatchesAreAcknowledgedWithoutEntitlement(t *testing.T) {
	testCases := []struct {
		name         string
		sessionID    string
		bindingToken string
		amountUnit   int64
		currency     string
	}{
		{name: "different session", sessionID: "cs_subscription_other", bindingToken: stripeWebhookBindingToken, amountUnit: 999, currency: "USD"},
		{name: "different binding token", sessionID: "cs_subscription_expected", bindingToken: "stripe_subscription_binding_other", amountUnit: 999, currency: "USD"},
		{name: "different amount", sessionID: "cs_subscription_expected", bindingToken: stripeWebhookBindingToken, amountUnit: 1000, currency: "USD"},
		{name: "different currency", sessionID: "cs_subscription_expected", bindingToken: stripeWebhookBindingToken, amountUnit: 999, currency: "CNY"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := setupStripeWebhookTest(t)
			tradeNo := "stripe-subscription-webhook-reject-" + tc.name
			userID := insertStripeWebhookSubscriptionOrder(t, db, tradeNo, "cs_subscription_expected")
			payload := stripeCheckoutCompletedPayloadWithMetadataKey(
				tradeNo,
				tc.sessionID,
				stripeSubscriptionBindingMetadataKey,
				tc.bindingToken,
				tc.amountUnit,
				tc.currency,
				"paid",
			)

			response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
			assert.Equal(t, http.StatusOK, response.Code)
			stored := model.GetSubscriptionOrderByTradeNo(tradeNo)
			require.NotNil(t, stored)
			assert.Equal(t, common.TopUpStatusPending, stored.Status)
			assert.Equal(t, "cs_subscription_expected", stored.ExpectedSessionID)
			var count int64
			require.NoError(t, db.Model(&model.UserSubscription{}).Where("user_id = ?", userID).Count(&count).Error)
			assert.Zero(t, count)
			assert.Nil(t, model.GetTopUpByTradeNo(tradeNo))
		})
	}
}

func TestStripeWebhook_SubscriptionLegacyOrderFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupStripeWebhookTest(t)
	const tradeNo = "stripe-subscription-webhook-legacy"
	const sessionID = "cs_subscription_legacy"
	userID := insertStripeWebhookSubscriptionOrder(t, db, tradeNo, sessionID)
	order := model.GetSubscriptionOrderByTradeNo(tradeNo)
	require.NotNil(t, order)
	order.PaymentExpectationVersion = 0
	order.ExpectedAmountUnit = 0
	order.ExpectedCurrency = ""
	order.ExpectedSessionID = ""
	order.ExpectedBindingToken = ""
	order.EntitlementSnapshotVersion = 0
	order.EntitlementSnapshot = ""
	require.NoError(t, order.Update())
	payload := stripeCheckoutCompletedPayloadWithMetadataKey(
		tradeNo,
		sessionID,
		stripeSubscriptionBindingMetadataKey,
		stripeWebhookBindingToken,
		999,
		"USD",
		"paid",
	)

	response := invokeStripeWebhook(signedStripeWebhookRequest(t, payload))
	assert.Equal(t, http.StatusOK, response.Code)
	stored := model.GetSubscriptionOrderByTradeNo(tradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusPending, stored.Status)
	assert.Empty(t, stored.ExpectedSessionID)
	var count int64
	require.NoError(t, db.Model(&model.UserSubscription{}).Where("user_id = ?", userID).Count(&count).Error)
	assert.Zero(t, count)
	assert.Nil(t, model.GetTopUpByTradeNo(tradeNo))
}

func TestStripeRequestPay_PersistsExpectationBeforeCheckoutAndSessionAfterward(t *testing.T) {
	withStripeWalletQuoteSettings(t)

	previousDB := model.DB
	previousDatabaseType := common.MainDatabaseType()
	previousAPISecret := setting.StripeApiSecret
	previousMinTopUp := setting.StripeMinTopUp
	previousCreateSession := createStripeCheckoutSession
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	setting.StripeApiSecret = "sk_test_request_pay"
	setting.StripeMinTopUp = 1
	setting.StripeUnitPrice = 2.5
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{10: 0.8}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"vip":1.2}`))
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousDatabaseType)
		setting.StripeApiSecret = previousAPISecret
		setting.StripeMinTopUp = previousMinTopUp
		createStripeCheckoutSession = previousCreateSession
	})

	user := &model.User{
		Id:       501,
		Username: "stripe-request-pay-user",
		Password: "unused",
		Status:   common.UserStatusEnabled,
		Group:    "vip",
		Email:    "user@example.com",
	}
	require.NoError(t, db.Create(user).Error)

	checkoutCalled := false
	var pendingBeforeCheckout model.TopUp
	var checkoutParams *stripe.CheckoutSessionParams
	createStripeCheckoutSession = func(params *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
		checkoutCalled = true
		checkoutParams = params
		require.NoError(t, db.Where("user_id = ?", user.Id).First(&pendingBeforeCheckout).Error)
		return &stripe.CheckoutSession{ID: "cs_request_pay", URL: "https://checkout.example.test/request-pay"}, nil
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/stripe/pay", nil)
	context.Set("id", user.Id)
	stripeAdaptor.RequestPay(context, &StripePayRequest{Amount: 10, PaymentMethod: model.PaymentMethodStripe})

	require.True(t, checkoutCalled)
	assert.Equal(t, common.TopUpStatusPending, pendingBeforeCheckout.Status)
	assert.Equal(t, model.StripePaymentExpectationVersion, pendingBeforeCheckout.PaymentExpectationVersion)
	assert.Equal(t, int64(2400), pendingBeforeCheckout.ExpectedAmountUnit)
	assert.Equal(t, int64(6_000_000), pendingBeforeCheckout.ExpectedCreditedQuota)
	assert.Equal(t, stripeWalletCurrency, pendingBeforeCheckout.ExpectedCurrency)
	assert.Empty(t, pendingBeforeCheckout.ExpectedSessionID)
	require.Len(t, pendingBeforeCheckout.ExpectedBindingToken, stripeWalletBindingTokenLength)
	require.NotNil(t, checkoutParams)
	assert.Equal(t, pendingBeforeCheckout.ExpectedBindingToken, checkoutParams.Metadata[stripeWalletBindingMetadataKey])
	assert.InDelta(t, 24, pendingBeforeCheckout.Money, 0.000001)
	assert.InDelta(t, 24, pendingBeforeCheckout.ExpectedAmount, 0.000001)

	var persisted model.TopUp
	require.NoError(t, db.Where("trade_no = ?", pendingBeforeCheckout.TradeNo).First(&persisted).Error)
	assert.Equal(t, "cs_request_pay", persisted.ExpectedSessionID)
	assert.Equal(t, pendingBeforeCheckout.ExpectedAmountUnit, persisted.ExpectedAmountUnit)
	assert.Equal(t, pendingBeforeCheckout.ExpectedCreditedQuota, persisted.ExpectedCreditedQuota)
	assert.Equal(t, pendingBeforeCheckout.ExpectedCurrency, persisted.ExpectedCurrency)
	assert.Equal(t, pendingBeforeCheckout.ExpectedBindingToken, persisted.ExpectedBindingToken)

	assert.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Message string `json:"message"`
		Data    struct {
			PayLink string `json:"pay_link"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "success", response.Message)
	assert.Equal(t, "https://checkout.example.test/request-pay", response.Data.PayLink)
	assert.NotContains(t, recorder.Body.String(), pendingBeforeCheckout.ExpectedBindingToken)
}
