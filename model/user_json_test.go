package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserJSONDoesNotExposeCredentialInputs(t *testing.T) {
	accessToken := "management-token"
	payload, err := common.Marshal(User{
		Password:         "password-hash",
		OriginalPassword: "current-password",
		VerificationCode: "email-code",
		AccessToken:      &accessToken,
		AdminPermissions: map[string]map[string]bool{
			"models": {"read": true},
		},
	})
	require.NoError(t, err)

	body := string(payload)
	assert.NotContains(t, body, "password-hash")
	assert.NotContains(t, body, "current-password")
	assert.NotContains(t, body, "email-code")
	assert.NotContains(t, body, "management-token")
	assert.NotContains(t, body, `"admin_permissions"`)
}

func TestUserJSONStillAcceptsCredentialInputs(t *testing.T) {
	var user User
	require.NoError(t, common.Unmarshal([]byte(`{"password":"new-password","original_password":"old-password","verification_code":"email-code","admin_permissions":{"models":{"read":true}}}`), &user))

	assert.Equal(t, "new-password", user.Password)
	assert.Equal(t, "old-password", user.OriginalPassword)
	assert.Equal(t, "email-code", user.VerificationCode)
	assert.Equal(t, map[string]map[string]bool{"models": {"read": true}}, user.AdminPermissions)
}
