package model

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	sqlmysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAccessTokenTestDatabase(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	require.Contains(t, []string{"sqlite", "mysql", "postgres"}, dialect)
	dsn := "local"
	if dialect == "sqlite" {
		previousPath := common.SQLitePath
		common.SQLitePath = t.TempDir() + "/access-token.db"
		t.Cleanup(func() { common.SQLitePath = previousPath })
	} else {
		dsn = os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
		require.NotEmpty(t, dsn, "selected access token dialect requires a real database")
		name := fmt.Sprintf("newapi_pat_%d", time.Now().UnixNano())
		var isolatedDSN string
		if dialect == "mysql" {
			config, err := sqlmysql.ParseDSN(dsn)
			require.NoError(t, err)
			require.Equal(t, "tcp", config.Net)
			host, _, err := net.SplitHostPort(config.Addr)
			require.NoError(t, err)
			ip := net.ParseIP(host)
			require.True(t, ip != nil && ip.IsLoopback(), "database tests only permit loopback instances")
			config.DBName = name
			isolatedDSN = config.FormatDSN()
		} else {
			parsed, err := url.Parse(dsn)
			require.NoError(t, err)
			require.Contains(t, []string{"postgres", "postgresql"}, parsed.Scheme)
			ip := net.ParseIP(parsed.Hostname())
			require.True(t, ip != nil && ip.IsLoopback(), "database tests only permit loopback instances")
			parsed.Path = "/" + name
			isolatedDSN = parsed.String()
		}
		t.Setenv("ACCESS_TOKEN_TEST_DSN", dsn)
		admin, _, err := chooseDB("ACCESS_TOKEN_TEST_DSN", false)
		require.NoError(t, err)
		connection, err := admin.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		statement := "CREATE DATABASE " + name
		if dialect == "mysql" {
			statement += " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
		}
		require.NoError(t, admin.Exec(statement).Error)
		require.NoError(t, connection.Close())
		dsn = isolatedDSN
		t.Logf("isolated access token database: %s (%s)", name, dialect)
	}
	t.Setenv("ACCESS_TOKEN_TEST_DSN", dsn)
	db, _, err := chooseDB("ACCESS_TOKEN_TEST_DSN", false)
	require.NoError(t, err)
	require.Equal(t, dialect, db.Dialector.Name(), "selected tests must use the requested engine")
	connection, err := db.DB()
	require.NoError(t, err)
	connection.SetMaxOpenConns(4)
	connection.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = connection.Close() })
	query := "SELECT version()"
	if dialect == "sqlite" {
		query = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("access token database: %s %s", dialect, version)
	return db
}

func setupAccessTokenModelTest(t *testing.T) {
	t.Helper()
	dialect := os.Getenv("TEST_ACCESS_TOKEN_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	db := newAccessTokenTestDatabase(t, dialect)
	useAccessTokenTestDatabases(t, db, db)
	require.NoError(t, db.AutoMigrate(&User{}, &UserAccessToken{}, &Option{}, &UserSession{}, &AuthFlow{},
		&ExternalIdentityClaim{}, &TwoFA{}, &TwoFABackupCode{}, &PasskeyCredential{}, &Token{}, &UserOAuthBinding{}, &Log{}, &AuditLog{}))
}

func useAccessTokenTestDatabases(t *testing.T, db, logDB *gorm.DB) {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousBatch := common.RedisEnabled, common.BatchUpdateEnabled
	previousDeadline := legacyAccessTokenRetireAt.Load()
	DB, LOG_DB = db, logDB
	types := map[string]common.DatabaseType{
		"sqlite": common.DatabaseTypeSQLite, "mysql": common.DatabaseTypeMySQL, "postgres": common.DatabaseTypePostgreSQL,
	}
	common.SetDatabaseTypes(types[db.Dialector.Name()], types[logDB.Dialector.Name()])
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	initCol()
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.BatchUpdateEnabled = previousRedis, previousBatch
		legacyAccessTokenRetireAt.Store(previousDeadline)
		initCol()
	})
}

// The released Base64 generator produced either 28 or 32 characters. Preserve
// the exact stored generation, including PostgreSQL CHAR(32) padding, across
// operations that must not rotate or revoke it.
func storedLegacyAccessTokenGeneration(t *testing.T, userID int) User {
	t.Helper()
	var stored User
	require.NoError(t, DB.Unscoped().Select("access_token", "access_token_created_at").First(&stored, userID).Error)
	return stored
}

func TestValidateAccessTokenSelectsOnlyAuthenticationIdentity(t *testing.T) {
	for _, token := range []string{"AAECAwQFBgcICQoLDA0ODxAREhMU", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYX"} {
		t.Run(fmt.Sprintf("%d_characters", len(token)), func(t *testing.T) {
			setupAccessTokenModelTest(t)
			legacyAccessTokenRetireAt.Store(time.Now().Add(time.Hour).Unix())
			user := User{
				Username:    "pat-identity-user",
				Password:    "password-hash",
				Role:        common.RoleCommonUser,
				Status:      common.UserStatusEnabled,
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
		})
	}
}

func TestCreateUserAccessTokenRecordsUnixSecondsAndPreservesLegacyGeneration(t *testing.T) {
	for _, legacyToken := range []string{"AAECAwQFBgcICQoLDA0ODxAREhMU", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYX"} {
		t.Run(fmt.Sprintf("%d_characters", len(legacyToken)), func(t *testing.T) {
			setupAccessTokenModelTest(t)
			createdAt := int64(123)
			user := User{Username: "pat-generation-user", Status: common.UserStatusEnabled, AuthVersion: 1, AccessToken: &legacyToken, AccessTokenCreatedAt: &createdAt}
			require.NoError(t, DB.Create(&user).Error)
			previousGeneration := storedLegacyAccessTokenGeneration(t, user.Id)
			require.NotNil(t, previousGeneration.AccessToken)
			assert.Equal(t, AccessTokenFingerprint(legacyToken), AccessTokenFingerprint(previousGeneration.GetAccessToken()))
			assert.Equal(t, AccessTokenHint(legacyToken), AccessTokenHint(previousGeneration.GetAccessToken()))
			raw := "nap_generation_test_token"
			token := UserAccessToken{Name: "generation", TokenHash: AccessTokenFingerprint(raw), TokenHint: AccessTokenHint(raw)}
			require.NoError(t, token.SetScopes([]string{"profile:read"}))

			before := time.Now().Unix()
			require.NoError(t, CreateUserAccessToken(user.Id, &token, 20))
			after := time.Now().Unix()
			storedToken, err := GetUserAccessToken(user.Id, token.Id)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, storedToken.CreatedAt, before)
			assert.LessOrEqual(t, storedToken.CreatedAt, after)
			assert.Equal(t, AccessTokenFingerprint(raw), storedToken.TokenHash)
			assert.Equal(t, AccessTokenHint(raw), storedToken.TokenHint)
			encoded, err := common.Marshal(storedToken)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), raw)
			assert.Equal(t, previousGeneration, storedLegacyAccessTokenGeneration(t, user.Id))

			updated, err := UpdateUserAccessToken(user.Id, token.Id, "renamed", nil)
			require.NoError(t, err)
			assert.Equal(t, "renamed", updated.Name)
			assert.Equal(t, storedToken.CreatedAt, updated.CreatedAt)
			assert.Equal(t, storedToken.TokenHash, updated.TokenHash)
			assert.Equal(t, []string{"profile:read"}, updated.GetScopes())
			stored := storedLegacyAccessTokenGeneration(t, user.Id)
			assert.Equal(t, previousGeneration, stored)
			require.NotNil(t, stored.AccessTokenCreatedAt)
			assert.Equal(t, createdAt, *stored.AccessTokenCreatedAt)
		})
	}
}

func TestUpdateUserAccessTokenFailurePreservesStoredGrantAndLegacyGeneration(t *testing.T) {
	for _, oldToken := range []string{"AAECAwQFBgcICQoLDA0ODxAREhMU", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYX"} {
		t.Run(fmt.Sprintf("%d_characters", len(oldToken)), func(t *testing.T) {
			setupAccessTokenModelTest(t)
			createdAt := int64(123)
			user := User{Username: "pat-generation-failure", Status: common.UserStatusEnabled, AuthVersion: 1, AccessToken: &oldToken, AccessTokenCreatedAt: &createdAt}
			require.NoError(t, DB.Create(&user).Error)
			previousGeneration := storedLegacyAccessTokenGeneration(t, user.Id)
			require.NotNil(t, previousGeneration.AccessToken)
			assert.Equal(t, AccessTokenFingerprint(oldToken), AccessTokenFingerprint(previousGeneration.GetAccessToken()))
			token := createTestUserAccessToken(t, user.Id, "nap_update_failure_token", 0)
			require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("access_token_update_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "user_access_tokens" {
					tx.AddError(errors.New("forced access-token update failure"))
				}
			}))
			t.Cleanup(func() { DB.Callback().Update().Remove("access_token_update_failure") })

			updated, err := UpdateUserAccessToken(user.Id, token.Id, "new name", []string{"user:write"})
			require.Error(t, err)
			assert.Nil(t, updated)
			storedToken, err := GetUserAccessToken(user.Id, token.Id)
			require.NoError(t, err)
			assert.Equal(t, token, *storedToken)
			stored := storedLegacyAccessTokenGeneration(t, user.Id)
			assert.Equal(t, previousGeneration, stored)
			require.NotNil(t, stored.AccessTokenCreatedAt)
			assert.Equal(t, createdAt, *stored.AccessTokenCreatedAt)
		})
	}
}

func TestRevokeUserAccessTokenReturnsFingerprintOnlyAfterCommit(t *testing.T) {
	setupAccessTokenModelTest(t)
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
	for _, token := range []string{"AAECAwQFBgcICQoLDA0ODxAREhMU", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYX"} {
		t.Run(fmt.Sprintf("%d_characters", len(token)), func(t *testing.T) {
			setupAccessTokenModelTest(t)
			createdAt := int64(789)
			user := User{Username: "pat-revoke-failure", Password: "password", Status: common.UserStatusEnabled, AccessToken: &token, AccessTokenCreatedAt: &createdAt}
			require.NoError(t, DB.Create(&user).Error)
			previousGeneration := storedLegacyAccessTokenGeneration(t, user.Id)
			require.NotNil(t, previousGeneration.AccessToken)
			assert.Equal(t, AccessTokenFingerprint(token), AccessTokenFingerprint(previousGeneration.GetAccessToken()))
			require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("access_token_revoke_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "users" {
					tx.AddError(errors.New("forced access-token revoke failure"))
				}
			}))
			t.Cleanup(func() { DB.Callback().Update().Remove("access_token_revoke_failure") })

			ref, err := RevokeUserAccessToken(user.Id)
			require.Error(t, err)
			assert.Empty(t, ref)

			stored := storedLegacyAccessTokenGeneration(t, user.Id)
			assert.Equal(t, previousGeneration, stored)
			require.NotNil(t, stored.AccessTokenCreatedAt)
			assert.Equal(t, createdAt, *stored.AccessTokenCreatedAt)
		})
	}
}
