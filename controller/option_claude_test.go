package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionsOnlyAcceptsCompleteAccountGroupPair(t *testing.T) {
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousOptionMap := common.OptionMap
	previousRedisEnabled := common.RedisEnabled
	originalGroups := setting.AccountGroups2JSONString()
	originalDefault := setting.GetDefaultUserGroup()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Option{}, &model.Log{}, &model.User{}))
	model.DB = database
	model.LOG_DB = database
	common.OptionMap = map[string]string{}
	// The audit path reads the user cache; without Redis the recordManageAudit
	// call falls back to the in-memory database instead of a nil RDB client.
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.OptionMap = previousOptionMap
		common.RedisEnabled = previousRedisEnabled
		require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(originalGroups, originalDefault))
	})

	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPut, "/api/option/bulk", strings.NewReader(body))
		UpdateOptions(context)
		return response
	}

	unknownKey := put(`{"options":{"AccountGroups":"{\"default\":\"Default\"}","DefaultUserGroup":"default","GroupRatio":"{}"}}`)
	assert.Equal(t, http.StatusOK, unknownKey.Code)
	assert.Contains(t, unknownKey.Body.String(), `"success":false`)
	assert.Contains(t, unknownKey.Body.String(), "仅支持")

	incomplete := put(`{"options":{"AccountGroups":"{\"default\":\"Default\"}"}}`)
	assert.Equal(t, http.StatusOK, incomplete.Code)
	assert.Contains(t, incomplete.Body.String(), `"success":false`)
	assert.Contains(t, incomplete.Body.String(), "必须同时包含")

	accepted := put(`{"options":{"AccountGroups":"{\"vip\":\"VIP\"}","DefaultUserGroup":"vip"}}`)
	assert.Equal(t, http.StatusOK, accepted.Code)
	assert.Contains(t, accepted.Body.String(), `"success":true`)
	assert.Equal(t, []string{"vip"}, setting.GetSortedAccountGroupIDs())
	assert.Equal(t, "vip", setting.GetDefaultUserGroup())
	assert.Equal(t, `{"vip":"VIP"}`, requirePersistedOption(t, database, setting.AccountGroupsOptionKey))
	assert.Equal(t, "vip", requirePersistedOption(t, database, "DefaultUserGroup"))
}

func requirePersistedOption(t *testing.T, database *gorm.DB, key string) string {
	t.Helper()
	var option model.Option
	require.NoError(t, database.Where("key = ?", key).First(&option).Error)
	return option.Value
}

func TestUpdateOptionRejectsNegativeClaudeDefaultMaxTokens(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(`{"key":"claude.default_max_tokens","value":"{\"default\":-1}"}`),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.Contains(t, payload.Message, "-1")
}

func TestUpdateOptionRejectsNegativeBillingRatios(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "GroupGroupRatio", value: `{"default":{"default":-0.5}}`},
		{key: "TopupGroupRatio", value: `{"default":-0.5}`},
	}

	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			body, err := common.Marshal(map[string]any{"key": test.key, "value": test.value})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))

			UpdateOption(context)

			assert.Equal(t, http.StatusOK, response.Code)
			var payload struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.False(t, payload.Success)
			assert.Contains(t, payload.Message, "not less than 0")
		})
	}
}
