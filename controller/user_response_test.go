package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetUserReturnsAdminPermissionsWithoutCredentialFields(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}))
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	accessToken := "management-token"
	user := &model.User{
		Username:         "detail-user",
		Password:         "password-hash",
		OriginalPassword: "current-password",
		VerificationCode: "email-code",
		AccessToken:      &accessToken,
		DisplayName:      "Detail User",
		Role:             common.RoleCommonUser,
		Status:           common.UserStatusEnabled,
		Group:            "default",
	}
	require.NoError(t, db.Create(user).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/user/1", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Set("role", common.RoleRootUser)

	GetUser(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Contains(t, response.Data, "admin_permissions")
	assert.NotContains(t, string(recorder.Body.Bytes()), "password-hash")
	assert.NotContains(t, string(recorder.Body.Bytes()), "current-password")
	assert.NotContains(t, string(recorder.Body.Bytes()), "email-code")
	assert.NotContains(t, string(recorder.Body.Bytes()), "management-token")
}
