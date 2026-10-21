package model

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MigrationIdentityFields struct {
	ID        int    `gorm:"primaryKey"`
	Name      string `gorm:"size:64;unique"`
	Reference string `gorm:"size:64;uniqueIndex"`
	Provider  string `gorm:"size:32;uniqueIndex:,composite:provider_subject"`
	Subject   string `gorm:"size:64;uniqueIndex:,composite:provider_subject"`
}

type migrationIdentityV1 struct {
	MigrationIdentityFields
	Digest string `gorm:"type:char(32)"`
}

type migrationIdentityV2 struct {
	MigrationIdentityFields
	Digest string `gorm:"type:char(64)"`
	Note   string `gorm:"size:128"`
}

type migrationConstraintV1 struct {
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"size:64"`
}

type migrationConstraintV2 struct {
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"size:64;unique"`
}

type migrationDecimalV1 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(10,6);default:0"`
}

type migrationDecimalV2 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(12,6);not null;default:0"`
}

type migrationDecimalV3 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(12,6);not null;default:1.25"`
}

// Matches the TaskPlugin schema shipped in v1.0.0-rc.38-th.22.
type releasedTaskPlugin struct {
	Id         int64
	Key        string `gorm:"size:128;not null;uniqueIndex:uk_task_plugin_key_version,priority:1"`
	APIVersion int    `gorm:"not null"`
	Version    string `gorm:"size:64;not null;uniqueIndex:uk_task_plugin_key_version,priority:2"`
	Source     string `gorm:"type:text;not null"`
	SourceHash string `gorm:"size:64;not null"`
	Icon       string `gorm:"size:524288"`
	Enabled    bool   `gorm:"not null"`
	Active     bool   `gorm:"not null;index"`
	CreatedAt  int64  `gorm:"not null"`
	Remark     string `gorm:"type:text"`
}

// Persisted authentication/account fields from v1.0.0-rc.38-th.22, not the
// current User embedded into an "old" schema.
type releasedAccessTokenUser struct {
	Id                   int
	Username             string         `gorm:"unique;index"`
	Password             string         `gorm:"not null;"`
	DisplayName          string         `gorm:"index"`
	Role                 int            `gorm:"type:int;default:1"`
	Status               int            `gorm:"type:int;default:1"`
	Email                string         `gorm:"index"`
	GitHubId             string         `gorm:"column:github_id;index"`
	DiscordId            string         `gorm:"column:discord_id;index"`
	OidcId               string         `gorm:"column:oidc_id;index"`
	WeChatId             string         `gorm:"column:wechat_id;index"`
	TelegramId           string         `gorm:"column:telegram_id;index"`
	AccessToken          *string        `gorm:"type:char(32);column:access_token;uniqueIndex"`
	AccessTokenCreatedAt *int64         `gorm:"type:bigint;column:access_token_created_at"`
	Quota                int64          `gorm:"type:bigint;default:0"`
	UsedQuota            int            `gorm:"type:int;default:0;column:used_quota"`
	RequestCount         int            `gorm:"type:int;default:0;"`
	Group                string         `gorm:"type:varchar(64);default:'default'"`
	AffCode              string         `gorm:"type:varchar(32);column:aff_code;uniqueIndex"`
	AffCount             int            `gorm:"type:int;default:0;column:aff_count"`
	AffQuota             int            `gorm:"type:int;default:0;column:aff_quota"`
	AffHistoryQuota      int            `gorm:"type:int;default:0;column:aff_history"`
	InviterId            int            `gorm:"type:int;column:inviter_id;index"`
	DeletedAt            gorm.DeletedAt `gorm:"index"`
	LinuxDOId            string         `gorm:"column:linux_do_id;index"`
	Setting              string         `gorm:"type:text;column:setting"`
	Remark               string         `gorm:"type:varchar(255)"`
	StripeCustomer       string         `gorm:"type:varchar(64);column:stripe_customer;index"`
	CreatedAt            int64          `gorm:"autoCreateTime;column:created_at"`
	LastLoginAt          int64          `gorm:"default:0;column:last_login_at"`
	AuthVersion          int64          `gorm:"type:bigint;not null;default:1;column:auth_version"`
}

func (releasedAccessTokenUser) TableName() string { return "users" }

type releasedAccessTokenOption struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

func (releasedAccessTokenOption) TableName() string { return "options" }

type releasedAccessTokenAuditLog struct {
	Id         int
	EventId    string `gorm:"type:varchar(64);uniqueIndex"`
	UserId     int    `gorm:"index:idx_audit_user_time,priority:1"`
	Username   string `gorm:"type:varchar(64);index"`
	ActorRole  int
	CreatedAt  int64  `gorm:"type:bigint;index:idx_audit_user_time,priority:2;index:idx_audit_token_time,priority:2;index"`
	Category   string `gorm:"type:varchar(24);index"`
	Action     string `gorm:"type:varchar(128)"`
	TokenRef   string `gorm:"type:varchar(64);index:idx_audit_token_time,priority:1"`
	AuthMethod string `gorm:"type:varchar(24)"`
	Ip         string `gorm:"type:varchar(64)"`
	UserAgent  string `gorm:"type:varchar(512)"`
	Method     string `gorm:"type:varchar(16)"`
	Route      string `gorm:"type:varchar(255)"`
	Status     int
	Success    bool
	RequestId  string     `gorm:"type:varchar(64);index"`
	Content    string     `gorm:"type:text"`
	Other      AuditOther `gorm:"type:json"`
}

func (releasedAccessTokenAuditLog) TableName() string { return "audit_logs" }

// releasedQuotaData mirrors the quota_data schema shipped in v1.0.0-rc.44-th.23
// and v1.0.0-rc.45, before the dashboard aggregation table grew the cache token
// counters.
type releasedQuotaData struct {
	Id        int    `json:"id"`
	UserID    int    `json:"user_id" gorm:"index"`
	Username  string `json:"username" gorm:"index:idx_qdt_model_user_name,priority:2;size:64;default:''"`
	ModelName string `json:"model_name" gorm:"index:idx_qdt_model_user_name,priority:1;size:64;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;index:idx_qdt_created_at,priority:2"`
	UseGroup  string `json:"use_group" gorm:"index;size:64;default:''"`
	TokenID   int    `json:"token_id" gorm:"index;default:0"`
	ChannelID int    `json:"channel_id" gorm:"index;default:0"`
	NodeName  string `json:"node_name" gorm:"index;size:64;default:''"`
	TokenUsed int    `json:"token_used" gorm:"default:0"`
	Count     int    `json:"count" gorm:"default:0"`
	Quota     int    `json:"quota" gorm:"default:0"`
}

func (releasedQuotaData) TableName() string { return "quota_data" }

func TestAccessTokenMigrationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			if dialect != "sqlite" && os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN") == "" {
				t.Skip("test database DSN is not configured")
			}
			for _, phase := range []string{"fresh", "upgrade"} {
				t.Run(phase, func(t *testing.T) {
					db := newAccessTokenTestDatabase(t, dialect)
					logDB := newAccessTokenTestDatabase(t, dialect)
					useAccessTokenTestDatabases(t, db, logDB)
					t.Setenv("SKIP_64BIT_QUOTA_SCHEMA_CHECK", "false")
					legacyToken := "0123456789abcdef0123456789abcdef"
					legacyCreatedAt := int64(1_700_000_000)
					retained := User{
						Id: 7, Username: "released-pat-user", Password: "retained-hash", DisplayName: "保留用户",
						Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Email: "retained@example.com",
						GitHubId: "retained-github", AccessToken: &legacyToken, AccessTokenCreatedAt: &legacyCreatedAt,
						Quota: 4_500_000_123, UsedQuota: 456, RequestCount: 12, Group: "retained-account",
						AffCode: "retained-aff", AffCount: 3, AffQuota: 123, AffHistoryQuota: 789,
						Setting: `{"language":"zh"}`, Remark: "retained note", StripeCustomer: "retained-customer",
						CreatedAt: legacyCreatedAt, LastLoginAt: legacyCreatedAt + 60, AuthVersion: 7,
					}
					shortLegacyToken := "AAECAwQFBgcICQoLDA0ODxAREhMU"
					shortRetained := User{
						Id: 8, Username: "released-short-pat", Password: "retained-short-hash",
						Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
						AccessToken: &shortLegacyToken, AccessTokenCreatedAt: &legacyCreatedAt,
						Quota: 4_500_000_124, Group: "retained-account", AffCode: "retained-short-aff",
						CreatedAt: legacyCreatedAt, AuthVersion: 8,
					}
					var shortGeneration User
					retainedAudit := AuditLog{
						EventId: "retained-audit", UserId: retained.Id, Username: retained.Username, ActorRole: retained.Role,
						CreatedAt: legacyCreatedAt, Category: AuditCategoryAccessToken, Action: "access_token.use",
						TokenRef: AccessTokenFingerprint(legacyToken), AuthMethod: "access_token", Success: true,
						RequestId: "retained-request", Other: AuditOther{LoginMethod: "password"},
					}
					retainedLog := Log{UserId: retained.Id, CreatedAt: legacyCreatedAt, Type: LogTypeConsume, Content: "retained usage", Quota: 123}
					if phase == "upgrade" {
						require.NoError(t, db.AutoMigrate(&releasedAccessTokenUser{}, &releasedAccessTokenOption{}))
						legacy := releasedAccessTokenUser{
							Id: retained.Id, Username: retained.Username, Password: retained.Password, DisplayName: retained.DisplayName,
							Role: retained.Role, Status: retained.Status, Email: retained.Email, GitHubId: retained.GitHubId,
							AccessToken: retained.AccessToken, AccessTokenCreatedAt: retained.AccessTokenCreatedAt, Quota: retained.Quota,
							UsedQuota: retained.UsedQuota, RequestCount: retained.RequestCount, Group: retained.Group,
							AffCode: retained.AffCode, AffCount: retained.AffCount, AffQuota: retained.AffQuota, AffHistoryQuota: retained.AffHistoryQuota,
							Setting: retained.Setting, Remark: retained.Remark, StripeCustomer: retained.StripeCustomer,
							CreatedAt: retained.CreatedAt, LastLoginAt: retained.LastLoginAt, AuthVersion: retained.AuthVersion,
						}
						require.NoError(t, db.Create(&legacy).Error)
						shortLegacy := releasedAccessTokenUser{
							Id: shortRetained.Id, Username: shortRetained.Username, Password: shortRetained.Password,
							Role: shortRetained.Role, Status: shortRetained.Status, AccessToken: shortRetained.AccessToken,
							AccessTokenCreatedAt: shortRetained.AccessTokenCreatedAt, Quota: shortRetained.Quota,
							Group: shortRetained.Group, AffCode: shortRetained.AffCode,
							CreatedAt: shortRetained.CreatedAt, AuthVersion: shortRetained.AuthVersion,
						}
						require.NoError(t, db.Create(&shortLegacy).Error)
						shortGeneration = storedLegacyAccessTokenGeneration(t, shortRetained.Id)
						require.NoError(t, db.Create(&releasedAccessTokenOption{Key: "RetainedSetting", Value: "retained-value"}).Error)
						require.NoError(t, logDB.AutoMigrate(&releasedAccessTokenAuditLog{}, &Log{})) // Log is unchanged from the release.
						require.NoError(t, logDB.Create(&retainedAudit).Error)
						require.NoError(t, logDB.Create(&retainedLog).Error)
						assert.False(t, db.Migrator().HasTable(&UserAccessToken{}))
					}

					if dialect == "mysql" {
						require.NoError(t, checkMySQLChineseSupport(db))
						require.NoError(t, checkMySQLChineseSupport(logDB))
					}
					require.NoError(t, ensureUserQuotaColumns(db, common.MainDatabaseType()), "startup must accept the released wallet schema")
					before := time.Now().Unix()
					require.NoError(t, migrateDB())
					require.NoError(t, migrateLOGDB())
					deadline := LegacyAccessTokenRetireAt()
					assert.GreaterOrEqual(t, deadline, before+30*24*60*60)
					assert.LessOrEqual(t, deadline, time.Now().Unix()+30*24*60*60)
					if phase == "fresh" {
						require.NoError(t, db.Create(&retained).Error)
						require.NoError(t, db.Create(&shortRetained).Error)
						shortGeneration = storedLegacyAccessTokenGeneration(t, shortRetained.Id)
						require.NoError(t, logDB.Create(&retainedAudit).Error)
						require.NoError(t, logDB.Create(&retainedLog).Error)
					}
					require.NotNil(t, shortGeneration.AccessToken)
					assert.Equal(t, AccessTokenFingerprint(shortLegacyToken), AccessTokenFingerprint(shortGeneration.GetAccessToken()))
					assert.Equal(t, AccessTokenHint(shortLegacyToken), AccessTokenHint(shortGeneration.GetAccessToken()))
					require.NotNil(t, shortGeneration.AccessTokenCreatedAt)
					assert.Equal(t, legacyCreatedAt, *shortGeneration.AccessTokenCreatedAt)
					created := createTestUserAccessToken(t, retained.Id, "nap_migration_contract_token", before+3600)
					mainRecorder, logRecorder := &migrationSQLRecorder{}, &migrationSQLRecorder{}
					DB = db.Session(&gorm.Session{Logger: mainRecorder})
					LOG_DB = logDB.Session(&gorm.Session{Logger: logRecorder})
					require.NoError(t, ensureUserQuotaColumns(DB, common.MainDatabaseType()), "a second startup must accept the generated schema")
					require.NoError(t, migrateDB())
					require.NoError(t, migrateLOGDB())
					assert.Empty(t, mainRecorder.schemaMutations(), "second startup must not change the main schema")
					assert.Empty(t, logRecorder.schemaMutations(), "second startup must not change the separate log schema")
					assert.Equal(t, deadline, LegacyAccessTokenRetireAt(), "restart must not extend the legacy transition")
					require.NoError(t, EnsureLegacyAccessTokenRetireAt(before+24*60*60))
					assert.Equal(t, deadline, LegacyAccessTokenRetireAt())
					var storedDeadline Option
					require.NoError(t, DB.Where(&Option{Key: legacyAccessTokenRetireAtKey}).First(&storedDeadline).Error)
					assert.Equal(t, strconv.FormatInt(deadline, 10), storedDeadline.Value)
					if phase == "upgrade" {
						var option Option
						require.NoError(t, DB.Where(&Option{Key: "RetainedSetting"}).First(&option).Error)
						assert.Equal(t, "retained-value", option.Value)
					}
					var storedUser User
					require.NoError(t, DB.First(&storedUser, retained.Id).Error)
					assert.Equal(t, retained, storedUser, "wallet, legacy generation, authentication and account data must survive")
					assert.Equal(t, shortGeneration, storedLegacyAccessTokenGeneration(t, shortRetained.Id), "the released 28-character generation must survive unchanged")
					var storedShortUser User
					require.NoError(t, DB.First(&storedShortUser, shortRetained.Id).Error)
					expectedShortUser := shortRetained
					expectedShortUser.AccessToken = shortGeneration.AccessToken
					assert.Equal(t, expectedShortUser, storedShortUser)
					for _, identity := range []struct {
						token string
						user  User
					}{{legacyToken, retained}, {shortLegacyToken, shortRetained}} {
						validated, err := ValidateAccessToken(identity.token)
						require.NoError(t, err)
						require.NotNil(t, validated)
						assert.Equal(t, identity.user.Id, validated.Id)
						assert.Equal(t, identity.user.AuthVersion, validated.AuthVersion)
						assert.Empty(t, validated.Password)
						assert.Nil(t, validated.AccessToken)
					}
					storedToken, err := GetUserAccessToken(retained.Id, created.Id)
					require.NoError(t, err)
					assert.Equal(t, created, *storedToken)
					var storedAudit AuditLog
					require.NoError(t, LOG_DB.First(&storedAudit, retainedAudit.Id).Error)
					assert.Equal(t, retainedAudit, storedAudit)
					var storedLog Log
					require.NoError(t, LOG_DB.First(&storedLog, retainedLog.Id).Error)
					assert.Equal(t, retainedLog, storedLog)
					assert.False(t, LOG_DB.Migrator().HasTable(&User{}))
					assert.False(t, LOG_DB.Migrator().HasTable(&UserAccessToken{}))
					for _, index := range []string{"idx_users_username", "idx_users_access_token", "idx_users_aff_code"} {
						assert.True(t, DB.Migrator().HasIndex(&User{}, index), index)
					}
					for _, index := range []string{"idx_user_access_tokens_user_id", "idx_user_access_tokens_token_hash", "idx_user_access_tokens_expires_at"} {
						assert.True(t, DB.Migrator().HasIndex(&UserAccessToken{}, index), index)
					}
					for _, index := range []string{"idx_audit_logs_event_id", "idx_audit_user_time", "idx_audit_token_time"} {
						assert.True(t, LOG_DB.Migrator().HasIndex(&AuditLog{}, index), index)
					}
					for _, field := range []string{"username", "access_token", "aff_code"} {
						duplicate := User{Username: "other-user", Password: "hash", AffCode: "other-aff"}
						switch field {
						case "username":
							duplicate.Username = retained.Username
						case "access_token":
							duplicate.AccessToken = retained.AccessToken
						case "aff_code":
							duplicate.AffCode = retained.AffCode
						}
						require.Error(t, DB.Create(&duplicate).Error, "released uniqueness must survive: %s", field)
					}
					duplicateToken := created
					duplicateToken.Id = 0
					require.Error(t, DB.Create(&duplicateToken).Error, "PAT fingerprint must remain unique")
					duplicateAudit := retainedAudit
					duplicateAudit.Id = 0
					require.Error(t, LOG_DB.Create(&duplicateAudit).Error, "audit event identity must remain unique")
				})
			}
		})
	}
}

// Upstream added the cache token counters to the dashboard aggregation table
// without a regression test. Prove the schema change on a fresh database and
// on a database upgraded from the latest release, on every supported engine.
func TestQuotaDataCacheColumnsMigrationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			if dialect != "sqlite" && os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN") == "" {
				t.Skip("test database DSN is not configured")
			}
			for _, phase := range []string{"fresh", "upgrade"} {
				t.Run(phase, func(t *testing.T) {
					// An isolated database keeps the production table name, so the
					// real index names are exercised on every engine.
					db := newAccessTokenTestDatabase(t, dialect)
					useAccessTokenTestDatabases(t, db, db)
					resetQuotaDataCache := func() {
						CacheQuotaDataLock.Lock()
						CacheQuotaData = make(map[string]*QuotaData)
						CacheQuotaDataLock.Unlock()
					}
					resetQuotaDataCache()
					t.Cleanup(resetQuotaDataCache)

					released := releasedQuotaData{
						Id: 21, UserID: 7, Username: "alice", ModelName: "gpt-a", CreatedAt: 3600,
						UseGroup: "vip", TokenID: 11, ChannelID: 1, NodeName: "node-a",
						TokenUsed: 40, Count: 2, Quota: 100,
					}
					if phase == "upgrade" {
						require.NoError(t, db.AutoMigrate(&releasedQuotaData{}))
						require.False(t, db.Migrator().HasColumn(&QuotaData{}, "cache_read_tokens"))
						require.False(t, db.Migrator().HasColumn(&QuotaData{}, "cache_creation_tokens"))
						require.NoError(t, db.Create(&released).Error)
					}

					// The first startup migrates the released schema forward.
					require.NoError(t, ensureUserQuotaColumns(db, common.MainDatabaseType()))
					require.NoError(t, migrateDB())
					assert.True(t, db.Migrator().HasColumn(&QuotaData{}, "cache_read_tokens"))
					assert.True(t, db.Migrator().HasColumn(&QuotaData{}, "cache_creation_tokens"))
					for _, index := range []string{
						"idx_qdt_model_user_name", "idx_qdt_created_at",
						"idx_quota_data_user_id", "idx_quota_data_use_group",
						"idx_quota_data_token_id", "idx_quota_data_channel_id",
						"idx_quota_data_node_name",
					} {
						assert.True(t, db.Migrator().HasIndex(&QuotaData{}, index), index)
					}

					// A second startup must not change the schema again.
					recorder := &migrationSQLRecorder{}
					DB = db.Session(&gorm.Session{Logger: recorder})
					require.NoError(t, migrateDB())
					assert.Empty(t, recorder.schemaMutations(), "a repeated startup must not change the schema")

					if phase == "upgrade" {
						var stored QuotaData
						require.NoError(t, db.First(&stored, released.Id).Error)
						assert.Equal(t, released.Quota, stored.Quota, "the released aggregation row must survive the migration")
						assert.Equal(t, released.Count, stored.Count)
						assert.Equal(t, released.TokenUsed, stored.TokenUsed)
						assert.Equal(t, int64(0), stored.CacheReadTokens, "the new counters must default to zero")
						assert.Equal(t, int64(0), stored.CacheCreationTokens)
					}

					// Cache counters must be recorded and accumulated per bucket.
					for _, batch := range []QuotaDataLogParams{
						{
							UserID: 7, Username: "alice", ModelName: "gpt-a", CreatedAt: 3661,
							UseGroup: "vip", TokenID: 11, ChannelID: 1, NodeName: "node-a",
							Quota: 10, TokenUsed: 5, CacheReadTokens: 1234, CacheCreationTokens: 56,
						},
						{
							UserID: 7, Username: "alice", ModelName: "gpt-a", CreatedAt: 3700,
							UseGroup: "vip", TokenID: 11, ChannelID: 1, NodeName: "node-a",
							Quota: 20, TokenUsed: 8, CacheReadTokens: 999, CacheCreationTokens: 7,
						},
						{
							// Negative counters are clamped and must never lower the totals.
							UserID: 7, Username: "alice", ModelName: "gpt-a", CreatedAt: 3800,
							UseGroup: "vip", TokenID: 11, ChannelID: 1, NodeName: "node-a",
							Quota: 0, TokenUsed: 0, CacheReadTokens: -50, CacheCreationTokens: -20,
						},
					} {
						resetQuotaDataCache()
						LogQuotaData(batch)
						SaveQuotaDataCache()
					}

					expectedCount, expectedQuota, expectedTokenUsed := 3, 30, 13
					if phase == "upgrade" {
						expectedCount, expectedQuota, expectedTokenUsed = 5, 130, 53
					}
					var stored QuotaData
					require.NoError(t, db.Where(&QuotaData{UserID: 7, Username: "alice"}).First(&stored).Error)
					assert.Equal(t, int64(2233), stored.CacheReadTokens)
					assert.Equal(t, int64(63), stored.CacheCreationTokens)
					assert.Equal(t, expectedCount, stored.Count)
					assert.Equal(t, expectedQuota, stored.Quota)
					assert.Equal(t, expectedTokenUsed, stored.TokenUsed)

					// The dashboard read path must return the new counters.
					rows, err := GetQuotaDataByUsername("alice", 0, 7200)
					require.NoError(t, err)
					require.Len(t, rows, 1)
					assert.Equal(t, int64(2233), rows[0].CacheReadTokens)
					assert.Equal(t, int64(63), rows[0].CacheCreationTokens)
					assert.Equal(t, expectedQuota, rows[0].Quota)

					// The counters must be stored as 64-bit integers on every engine.
					large := QuotaData{
						UserID: 9, Username: "bob", ModelName: "gpt-b", CreatedAt: 3600,
						UseGroup: "default", TokenID: 22, ChannelID: 2, NodeName: "node-b",
						Count: 1, Quota: 5, TokenUsed: 2,
						CacheReadTokens: 9_007_199_254_740_993, CacheCreationTokens: 4_294_967_296,
					}
					require.NoError(t, db.Create(&large).Error)
					var storedLarge QuotaData
					require.NoError(t, db.First(&storedLarge, large.Id).Error)
					assert.Equal(t, large, storedLarge)

					if dialect != "sqlite" {
						columns, err := db.Migrator().ColumnTypes(&QuotaData{})
						require.NoError(t, err)
						for _, column := range columns {
							if column.Name() != "cache_read_tokens" && column.Name() != "cache_creation_tokens" {
								continue
							}
							assert.True(t, is64BitIntegerType(common.MainDatabaseType(), column.DatabaseTypeName()),
								"%s must be a 64-bit integer, got %s", column.Name(), column.DatabaseTypeName())
						}
					}
				})
			}
		})
	}
}

func TestMigrationSchemaStability(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dsn string
			switch dialect {
			case "sqlite":
				dsn = "local"
				previousPath := common.SQLitePath
				common.SQLitePath = filepath.Join(t.TempDir(), "migration.db")
				t.Cleanup(func() { common.SQLitePath = previousPath })
			case "mysql":
				dsn = os.Getenv("TEST_MYSQL_DSN")
			case "postgres":
				dsn = os.Getenv("TEST_POSTGRES_DSN")
			}
			if dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			t.Setenv("MIGRATION_TEST_DSN", dsn)
			db, _, err := chooseDB("MIGRATION_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			recorder := &migrationSQLRecorder{}
			db = db.Session(&gorm.Session{Logger: recorder})

			t.Run("task_plugin_payloads", func(t *testing.T) {
				for _, upgrade := range []bool{false, true} {
					phase := "fresh"
					if upgrade {
						phase = "upgrade"
					}
					t.Run(phase, func(t *testing.T) {
						const table = "migration_task_plugin_payloads"
						t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(table)) })
						tableDB := db.Table(table).Session(&gorm.Session{})
						legacy := releasedTaskPlugin{
							Key: "retained-plugin", APIVersion: 1, Version: "1.0.0",
							Source: "export const label = '保留插件';", SourceHash: "released-hash",
							Icon: "data:image/svg+xml;base64,PHN2Zy8+", Enabled: true, Active: true,
							CreatedAt: 1700000000, Remark: "retained operator note",
						}
						if upgrade {
							require.NoError(t, tableDB.AutoMigrate(&releasedTaskPlugin{}))
							require.NoError(t, tableDB.Create(&legacy).Error)
						}
						require.NoError(t, tableDB.AutoMigrate(&TaskPlugin{}))
						recorder.reset()
						require.NoError(t, tableDB.AutoMigrate(&TaskPlugin{}))
						assert.Empty(t, recorder.schemaMutations(), "repeated startup must not re-alter plugin payload columns")
						columns, err := tableDB.Migrator().ColumnTypes(&TaskPlugin{})
						require.NoError(t, err)
						payloadTypes := map[string]string{}
						for _, column := range columns {
							if column.Name() == "source" || column.Name() == "icon" {
								payloadTypes[column.Name()] = strings.ToLower(column.DatabaseTypeName())
							}
						}
						expectedType := "text"
						if dialect == "mysql" {
							expectedType = "longtext"
						}
						assert.Equal(t, map[string]string{"source": expectedType, "icon": expectedType}, payloadTypes)
						if upgrade {
							var retained TaskPlugin
							require.NoError(t, tableDB.First(&retained, legacy.Id).Error)
							assert.Equal(t, TaskPlugin{
								Id: legacy.Id, Key: legacy.Key, APIVersion: legacy.APIVersion, Version: legacy.Version,
								Source: LongText(legacy.Source), SourceHash: legacy.SourceHash, Icon: LongText(legacy.Icon),
								Enabled: legacy.Enabled, Active: legacy.Active, CreatedAt: legacy.CreatedAt, Remark: legacy.Remark,
							}, retained)
						}
						large := TaskPlugin{
							Key: legacy.Key, APIVersion: 1, Version: "2.0.0", SourceHash: "large-hash", Enabled: true,
							Source: LongText(strings.Repeat("// plugin payload\n", 4096)),
							Icon:   LongText("data:image/svg+xml;base64," + strings.Repeat("PHN2Zy8+", 10000)),
						}
						require.Greater(t, len(large.Source), 65535)
						require.Greater(t, len(large.Icon), 65535)
						require.NoError(t, tableDB.Create(&large).Error, "another version of the same plugin remains valid")
						duplicate := large
						duplicate.Id = 0
						require.Error(t, tableDB.Create(&duplicate).Error, "plugin key/version uniqueness must survive migration")
						recorder.reset()
						require.NoError(t, tableDB.AutoMigrate(&TaskPlugin{}))
						assert.Empty(t, recorder.schemaMutations())
						var stored TaskPlugin
						require.NoError(t, tableDB.First(&stored, large.Id).Error)
						assert.Equal(t, large, stored, "large source and icon must round-trip across repeated migration")
						assert.True(t, tableDB.Migrator().HasIndex(&TaskPlugin{}, "uk_task_plugin_key_version"))
						assert.True(t, tableDB.Migrator().HasIndex(&TaskPlugin{}, "Active"), "active-version lookup index must survive migration")
					})
				}
			})

			t.Run("identity_and_indexes", func(t *testing.T) {
				const table = "migration_identity_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV1{}))
				row := migrationIdentityV1{
					MigrationIdentityFields: MigrationIdentityFields{ID: 1, Name: "root", Reference: "token-reference", Provider: "oidc", Subject: "subject"},
					Digest:                  "old-digest",
				}
				require.NoError(t, db.Table(table).Create(&row).Error)
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV1{}))
				assert.Empty(t, recorder.schemaMutations())

				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV2{}))
				columns, err := db.Table(table).Migrator().ColumnTypes(&migrationIdentityV2{})
				require.NoError(t, err)
				for _, column := range columns {
					if column.Name() == "digest" {
						length, ok := column.Length()
						require.True(t, ok)
						assert.EqualValues(t, 64, length)
					}
				}
				assert.True(t, db.Table(table).Migrator().HasColumn(&migrationIdentityV2{}, "note"))
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV2{}))
				assert.Empty(t, recorder.schemaMutations())
				var saved migrationIdentityV2
				require.NoError(t, db.Table(table).First(&saved, 1).Error)
				assert.Equal(t, row.MigrationIdentityFields, saved.MigrationIdentityFields)
				expectedDigest := row.Digest
				if dialect == "postgres" {
					expectedDigest += strings.Repeat(" ", 64-len(row.Digest))
				}
				assert.Equal(t, expectedDigest, saved.Digest)
				for _, duplicate := range []migrationIdentityV2{
					{MigrationIdentityFields: MigrationIdentityFields{Name: "root", Reference: "other-1", Provider: "other", Subject: "1"}},
					{MigrationIdentityFields: MigrationIdentityFields{Name: "other-2", Reference: "token-reference", Provider: "other", Subject: "2"}},
					{MigrationIdentityFields: MigrationIdentityFields{Name: "other-3", Reference: "other-3", Provider: "oidc", Subject: "subject"}},
				} {
					assert.Error(t, db.Table(table).Create(&duplicate).Error)
				}
			})

			t.Run("unique_constraint_changes", func(t *testing.T) {
				const table = "migration_constraint_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV1{}))
				require.NoError(t, db.Table(table).Create(&migrationConstraintV1{Name: "existing"}).Error)
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV2{}))
				assert.Error(t, db.Table(table).Create(&migrationConstraintV2{Name: "existing"}).Error)
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV2{}))
				assert.Empty(t, recorder.schemaMutations())
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV1{}))
				require.NoError(t, db.Table(table).Create(&migrationConstraintV1{Name: "existing"}).Error)
			})

			if dialect == "postgres" {
				t.Run("renamed_unique_constraints", func(t *testing.T) {
					const table = "migration_renamed_unique"
					t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
					tableDB := db.Table(table).Session(&gorm.Session{})
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					original := migrationConstraintV1{Name: "existing"}
					require.NoError(t, tableDB.Create(&original).Error)
					for _, name := range []string{"models_model_name_key", `imported "model" name`} {
						require.NoError(t, db.Exec("ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (name)", clause.Table{Name: table}, clause.Column{Name: name}).Error)
					}
					require.NoError(t, db.Exec("ALTER TABLE ? ADD CONSTRAINT keep_composite UNIQUE (id, name)", clause.Table{Name: table}).Error)
					require.NoError(t, db.Exec("CREATE INDEX keep_name_lookup ON ? (name)", clause.Table{Name: table}).Error)
					// A unique field must keep the old names and still reject duplicates.
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV2{}))
					require.Error(t, tableDB.Create(&migrationConstraintV2{Name: "existing"}).Error)
					require.NoError(t, db.Exec("CREATE TABLE migration_unique_reference (name varchar(64) REFERENCES migration_renamed_unique(name))").Error)
					t.Cleanup(func() { _ = db.Migrator().DropTable("migration_unique_reference") })
					require.NoError(t, db.Exec("INSERT INTO migration_unique_reference (name) VALUES (?)", "existing").Error)
					require.Error(t, tableDB.AutoMigrate(&migrationConstraintV1{}), "dependent foreign keys must not be cascaded away")
					for _, name := range []string{"models_model_name_key", `imported "model" name`} {
						assert.True(t, tableDB.Migrator().HasConstraint(&migrationConstraintV1{}, name), "all old constraints must survive rollback")
					}
					var references int64
					require.NoError(t, db.Table("migration_unique_reference").Count(&references).Error)
					assert.EqualValues(t, 1, references)
					require.NoError(t, db.Migrator().DropTable("migration_unique_reference"))
					// Removing column uniqueness must resolve both actual constraint names.
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					recorder.reset()
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					assert.Empty(t, recorder.schemaMutations())
					require.NoError(t, tableDB.Create(&migrationConstraintV1{Name: "existing"}).Error)
					assert.True(t, tableDB.Migrator().HasConstraint(&migrationConstraintV1{}, "keep_composite"))
					assert.True(t, tableDB.Migrator().HasIndex(&migrationConstraintV1{}, "keep_name_lookup"))
					var rows []migrationConstraintV1
					require.NoError(t, tableDB.Order("id").Find(&rows).Error)
					require.Len(t, rows, 2)
					assert.Equal(t, original, rows[0])
				})
			}

			if dialect == "mysql" {
				t.Run("decimal_default_and_real_changes", func(t *testing.T) {
					const table = "migration_decimal_test"
					t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
					require.NoError(t, db.Table(table).AutoMigrate(&migrationDecimalV1{}))
					require.NoError(t, db.Table(table).Create(&migrationDecimalV1{ID: 1, Price: 12.345678}).Error)
					for _, target := range []any{&migrationDecimalV1{}, &migrationDecimalV2{}, &migrationDecimalV3{}} {
						require.NoError(t, db.Table(table).AutoMigrate(target))
						recorder.reset()
						require.NoError(t, db.Table(table).AutoMigrate(target))
						assert.Empty(t, recorder.schemaMutations())
					}
					columns, err := db.Table(table).Migrator().ColumnTypes(&migrationDecimalV3{})
					require.NoError(t, err)
					for _, column := range columns {
						if column.Name() == "price" {
							precision, scale, ok := column.DecimalSize()
							require.True(t, ok)
							assert.EqualValues(t, 12, precision)
							assert.EqualValues(t, 6, scale)
							nullable, ok := column.Nullable()
							require.True(t, ok)
							assert.False(t, nullable)
						}
					}
					require.NoError(t, db.Table(table).Create(&map[string]any{"id": 2}).Error)
					var prices []float64
					require.NoError(t, db.Table(table).Order("id").Pluck("price", &prices).Error)
					assert.Equal(t, []float64{12.345678, 1.25}, prices)
				})
			}
		})
	}
}
