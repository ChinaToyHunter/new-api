package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizeSubscriptionPlanCurrency(t *testing.T) {
	testCases := []struct {
		name     string
		currency string
		valid    bool
	}{
		{name: "defaults empty currency to USD", currency: "", valid: true},
		{name: "accepts USD", currency: "USD", valid: true},
		{name: "accepts case insensitive USD", currency: " usd ", valid: true},
		{name: "rejects CNY", currency: "CNY", valid: false},
		{name: "rejects other currencies", currency: "EUR", valid: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			currency, valid := normalizeSubscriptionPlanCurrency(testCase.currency)
			if valid != testCase.valid {
				t.Fatalf("normalizeSubscriptionPlanCurrency(%q) valid = %t, want %t", testCase.currency, valid, testCase.valid)
			}
			if valid && currency != "USD" {
				t.Fatalf("normalizeSubscriptionPlanCurrency(%q) currency = %q, want USD", testCase.currency, currency)
			}
		})
	}
}

func TestIsUSDSubscriptionCurrency(t *testing.T) {
	if !isUSDSubscriptionCurrency(" USD ") {
		t.Fatal("expected USD to be accepted")
	}
	if isUSDSubscriptionCurrency("CNY") {
		t.Fatal("expected CNY to be rejected")
	}
}

func TestAdminUpdateSubscriptionPlanPreservesHistoricalAccountGroups(t *testing.T) {
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainDBType := common.MainDatabaseType()
	previousLogDBType := common.LogDatabaseType()
	previousGroups := setting.AccountGroups2JSONString()
	previousDefaultGroup := setting.GetDefaultUserGroup()
	previousPaymentSetting := *operation_setting.GetPaymentSetting()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{}))
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(
		`{"default":"Default account","vip":"VIP account"}`,
		"default",
	))
	confirmPaymentComplianceForTest(t)

	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainDBType, previousLogDBType)
		require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(previousGroups, previousDefaultGroup))
		*operation_setting.GetPaymentSetting() = previousPaymentSetting
		sqlDB, closeErr := db.DB()
		if closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	plan := &model.SubscriptionPlan{
		Title:          "Legacy plan",
		PriceAmount:    9.99,
		Currency:       "USD",
		DurationUnit:   model.SubscriptionDurationMonth,
		DurationValue:  1,
		Enabled:        true,
		UpgradeGroup:   "legacy-upgrade",
		DowngradeGroup: "legacy-downgrade",
	}
	require.NoError(t, db.Create(plan).Error)
	entitlement := &model.UserSubscription{
		UserId:         77,
		PlanId:         plan.Id,
		UpgradeGroup:   plan.UpgradeGroup,
		PrevUserGroup:  "default",
		DowngradeGroup: plan.DowngradeGroup,
		Status:         "active",
	}
	require.NoError(t, db.Create(entitlement).Error)

	update := func(t *testing.T, candidate model.SubscriptionPlan) (bool, string) {
		t.Helper()
		payload, marshalErr := common.Marshal(AdminUpsertSubscriptionPlanRequest{Plan: candidate})
		require.NoError(t, marshalErr)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPut,
			"/api/subscription/plan/"+strconv.Itoa(plan.Id), strings.NewReader(string(payload)))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(plan.Id)}}
		AdminUpdateSubscriptionPlan(ctx)
		var response struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		return response.Success, response.Message
	}

	preserve := *plan
	preserve.Title = "Preserved legacy plan"
	success, message := update(t, preserve)
	require.True(t, success, message)
	var stored model.SubscriptionPlan
	require.NoError(t, db.First(&stored, plan.Id).Error)
	assert.Equal(t, "Preserved legacy plan", stored.Title)
	assert.Equal(t, "legacy-upgrade", stored.UpgradeGroup)
	assert.Equal(t, "legacy-downgrade", stored.DowngradeGroup)

	var storedEntitlement model.UserSubscription
	require.NoError(t, db.First(&storedEntitlement, entitlement.Id).Error)
	assert.Equal(t, entitlement.UpgradeGroup, storedEntitlement.UpgradeGroup)
	assert.Equal(t, entitlement.DowngradeGroup, storedEntitlement.DowngradeGroup)

	beforeRejected := stored
	rejected := stored
	rejected.UpgradeGroup = "another-legacy-upgrade"
	success, _ = update(t, rejected)
	assert.False(t, success)
	var afterRejected model.SubscriptionPlan
	require.NoError(t, db.First(&afterRejected, plan.Id).Error)
	assert.Equal(t, beforeRejected, afterRejected)

	valid := afterRejected
	valid.Title = "Valid account groups"
	valid.UpgradeGroup = "vip"
	valid.DowngradeGroup = "default"
	success, message = update(t, valid)
	require.True(t, success, message)
	require.NoError(t, db.First(&stored, plan.Id).Error)
	assert.Equal(t, "vip", stored.UpgradeGroup)
	assert.Equal(t, "default", stored.DowngradeGroup)

	cleared := stored
	cleared.UpgradeGroup = ""
	cleared.DowngradeGroup = ""
	success, message = update(t, cleared)
	require.True(t, success, message)
	require.NoError(t, db.First(&stored, plan.Id).Error)
	assert.Empty(t, stored.UpgradeGroup)
	assert.Empty(t, stored.DowngradeGroup)
}

func TestAdminCreateSubscriptionPlanRejectsUnknownAccountGroup(t *testing.T) {
	previousDB := model.DB
	previousGroups := setting.AccountGroups2JSONString()
	previousDefaultGroup := setting.GetDefaultUserGroup()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}))
	model.DB = db
	require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(
		`{"default":"Default account"}`,
		"default",
	))
	confirmPaymentComplianceForTest(t)
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(previousGroups, previousDefaultGroup))
		sqlDB, closeErr := db.DB()
		if closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	payload, err := common.Marshal(AdminUpsertSubscriptionPlanRequest{Plan: model.SubscriptionPlan{
		Title:         "Unknown group plan",
		Currency:      "USD",
		DurationUnit:  model.SubscriptionDurationMonth,
		DurationValue: 1,
		UpgradeGroup:  "not-an-account-group",
	}})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/subscription/plan", strings.NewReader(string(payload)))
	ctx.Request.Header.Set("Content-Type", "application/json")
	AdminCreateSubscriptionPlan(ctx)

	var response struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	var count int64
	require.NoError(t, db.Model(&model.SubscriptionPlan{}).Count(&count).Error)
	assert.Zero(t, count)
}
