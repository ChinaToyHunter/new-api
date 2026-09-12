package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerformSystemUpdateRejectsVersionWhenTestModeDisabled(t *testing.T) {
	previous := operation_setting.SystemUpdateTestModeEnabled
	operation_setting.SystemUpdateTestModeEnabled = false
	t.Cleanup(func() { operation_setting.SystemUpdateTestModeEnabled = previous })

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/system/update", strings.NewReader(`{"version":"v1.0.0"}`))

	PerformSystemUpdate(context)

	assert.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.Contains(t, payload.Message, "test mode is disabled")
}

func TestListSystemUpdateReleasesRejectsWhenTestModeDisabled(t *testing.T) {
	previous := operation_setting.SystemUpdateTestModeEnabled
	operation_setting.SystemUpdateTestModeEnabled = false
	t.Cleanup(func() { operation_setting.SystemUpdateTestModeEnabled = previous })

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/system/update/releases", nil)

	ListSystemUpdateReleases(context)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `"success":false`)
	assert.Contains(t, response.Body.String(), "test mode is disabled")
}
