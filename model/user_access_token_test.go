package model

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestValidateAccessTokenSelectsOnlyAuthenticationIdentity(t *testing.T) {
	truncateTables(t)
	token := "pat-identity-test"
	user := User{
		Username:    "pat-identity-user",
		Password:    "password-hash",
		Role:        10,
		Status:      1,
		AuthVersion: 7,
		AccessToken: &token,
		Email:       "private@example.com",
		Setting:     `{"private":true}`,
	}
	require.NoError(t, DB.Create(&user).Error)

	validated, err := ValidateAccessToken(token)
	require.NoError(t, err)
	require.NotNil(t, validated)
	assert.Equal(t, user.Id, validated.Id)
	assert.Equal(t, user.Username, validated.Username)
	assert.Equal(t, user.Role, validated.Role)
	assert.Equal(t, user.Status, validated.Status)
	assert.Equal(t, user.AuthVersion, validated.AuthVersion)
	assert.Empty(t, validated.Password)
	assert.Nil(t, validated.AccessToken)
	assert.Empty(t, validated.Email)
	assert.Empty(t, validated.Setting)
}

func TestUpdateUserAccessTokenRecordsUnixSecondGeneration(t *testing.T) {
	truncateTables(t)
	user := User{Username: "pat-generation-user", Password: "password", Status: 1}
	require.NoError(t, DB.Create(&user).Error)

	before := time.Now().Unix()
	require.NoError(t, UpdateUserAccessToken(user.Id, "generation-token"))
	after := time.Now().Unix()

	var stored User
	require.NoError(t, DB.Unscoped().Select("access_token", "access_token_created_at").First(&stored, user.Id).Error)
	require.Equal(t, "generation-token", stored.GetAccessToken())
	require.NotNil(t, stored.AccessTokenCreatedAt)
	assert.GreaterOrEqual(t, *stored.AccessTokenCreatedAt, before)
	assert.LessOrEqual(t, *stored.AccessTokenCreatedAt, after)
}

func TestUpdateUserAccessTokenFailurePreservesPreviousGeneration(t *testing.T) {
	truncateTables(t)
	createdAt := int64(123)
	oldToken := "old-generation-token"
	user := User{Username: "pat-generation-failure", Password: "password", Status: 1, AccessToken: &oldToken, AccessTokenCreatedAt: &createdAt}
	require.NoError(t, DB.Create(&user).Error)
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("access_token_update_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("forced access-token update failure"))
		}
	}))
	t.Cleanup(func() { DB.Callback().Update().Remove("access_token_update_failure") })

	require.Error(t, UpdateUserAccessToken(user.Id, "new-generation-token"))
	var stored User
	require.NoError(t, DB.Unscoped().Select("access_token", "access_token_created_at").First(&stored, user.Id).Error)
	assert.Equal(t, oldToken, stored.GetAccessToken())
	require.NotNil(t, stored.AccessTokenCreatedAt)
	assert.Equal(t, createdAt, *stored.AccessTokenCreatedAt)
}

func TestRevokeUserAccessTokenReturnsFingerprintOnlyAfterCommit(t *testing.T) {
	truncateTables(t)
	createdAt := int64(456)
	token := "revocable-generation-token"
	user := User{Username: "pat-revoke-user", Password: "password", Status: 1, AccessToken: &token, AccessTokenCreatedAt: &createdAt}
	require.NoError(t, DB.Create(&user).Error)

	ref, err := RevokeUserAccessToken(user.Id)
	require.NoError(t, err)
	assert.Equal(t, AccessTokenFingerprint(token), ref)

	var stored User
	require.NoError(t, DB.Unscoped().Select("access_token", "access_token_created_at").First(&stored, user.Id).Error)
	assert.Nil(t, stored.AccessToken)
	assert.Nil(t, stored.AccessTokenCreatedAt)
}

func TestRevokeUserAccessTokenFailurePreservesPreviousGeneration(t *testing.T) {
	truncateTables(t)
	createdAt := int64(789)
	token := "failed-revoke-generation-token"
	user := User{Username: "pat-revoke-failure", Password: "password", Status: 1, AccessToken: &token, AccessTokenCreatedAt: &createdAt}
	require.NoError(t, DB.Create(&user).Error)
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("access_token_revoke_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("forced access-token revoke failure"))
		}
	}))
	t.Cleanup(func() { DB.Callback().Update().Remove("access_token_revoke_failure") })

	ref, err := RevokeUserAccessToken(user.Id)
	require.Error(t, err)
	assert.Empty(t, ref)

	var stored User
	require.NoError(t, DB.Unscoped().Select("access_token", "access_token_created_at").First(&stored, user.Id).Error)
	assert.Equal(t, token, stored.GetAccessToken())
	require.NotNil(t, stored.AccessTokenCreatedAt)
	assert.Equal(t, createdAt, *stored.AccessTokenCreatedAt)
}
