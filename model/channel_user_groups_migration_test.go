package model

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type channelUserGroupsMigrationLegacy struct {
	Id                 int         `json:"id"`
	Type               int         `json:"type" gorm:"default:0"`
	Key                string      `json:"key" gorm:"not null"`
	OpenAIOrganization *string     `json:"openai_organization"`
	TestModel          *string     `json:"test_model"`
	Status             int         `json:"status" gorm:"default:1"`
	Name               string      `json:"name" gorm:"index"`
	Weight             *uint       `json:"weight" gorm:"default:0"`
	CreatedTime        int64       `json:"created_time" gorm:"bigint"`
	TestTime           int64       `json:"test_time" gorm:"bigint"`
	ResponseTime       int         `json:"response_time"`
	BaseURL            *string     `json:"base_url" gorm:"column:base_url;default:''"`
	Other              string      `json:"other"`
	Balance            float64     `json:"balance"`
	BalanceUpdatedTime int64       `json:"balance_updated_time" gorm:"bigint"`
	Models             string      `json:"models"`
	Group              string      `json:"group" gorm:"type:varchar(64);default:'default'"`
	UsedQuota          int64       `json:"used_quota" gorm:"bigint;default:0"`
	ModelMapping       *string     `json:"model_mapping" gorm:"type:text"`
	StatusCodeMapping  *string     `json:"status_code_mapping" gorm:"type:varchar(1024);default:''"`
	Priority           *int64      `json:"priority" gorm:"bigint;default:0"`
	AutoBan            *int        `json:"auto_ban" gorm:"default:1"`
	OtherInfo          string      `json:"other_info"`
	Tag                *string     `json:"tag" gorm:"index"`
	Setting            *string     `json:"setting" gorm:"type:text"`
	ParamOverride      *string     `json:"param_override" gorm:"type:text"`
	HeaderOverride     *string     `json:"header_override" gorm:"type:text"`
	Remark             *string     `json:"remark" gorm:"type:varchar(255)" validate:"max=255"`
	ChannelInfo        ChannelInfo `json:"channel_info" gorm:"type:json"`
	OtherSettings      string      `json:"settings" gorm:"column:settings"`
}

func testChannelUserGroupsMigration(t *testing.T, db *gorm.DB, recorder *migrationSQLRecorder) {
	t.Helper()

	freshName := fmt.Sprintf("channel_user_groups_fresh_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Migrator().DropTable(freshName) })
	require.NoError(t, db.Table(freshName).AutoMigrate(&Channel{}))
	annotation := "default,vip"
	fresh := Channel{Name: "fresh", Key: "fresh-key", Group: "default", UserGroups: &annotation}
	require.NoError(t, db.Table(freshName).Create(&fresh).Error)
	nilAnnotation := Channel{Id: 2, Name: "fresh-nil", Key: "fresh-nil-key", Group: "default"}
	require.NoError(t, db.Table(freshName).Create(&nilAnnotation).Error)
	var freshCount int64
	require.NoError(t, db.Table(freshName).Count(&freshCount).Error)
	require.EqualValues(t, 2, freshCount)
	var storedFresh Channel
	require.NoError(t, db.Table(freshName).First(&storedFresh, fresh.Id).Error)
	require.NotNil(t, storedFresh.UserGroups)
	assert.Equal(t, annotation, *storedFresh.UserGroups)
	var storedNil struct {
		UserGroups *string `gorm:"column:user_groups"`
	}
	require.NoError(t, db.Table(freshName).Select("user_groups").Where("id = ?", nilAnnotation.Id).Take(&storedNil).Error)
	assert.Nil(t, storedNil.UserGroups)
	assertUserGroupsColumn(t, db, freshName, true)
	assert.True(t, db.Migrator().HasIndex(freshName, db.NamingStrategy.IndexName(freshName, "name")))
	recorder.reset()
	require.NoError(t, db.Table(freshName).AutoMigrate(&Channel{}))
	assert.Empty(t, recorder.schemaMutations(), "a repeated fresh migration must not repeat schema DDL")

	upgradeName := fmt.Sprintf("channel_user_groups_upgrade_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Migrator().DropTable(upgradeName) })
	legacy := channelUserGroupsMigrationLegacy{Name: "legacy", Key: "legacy-key", Group: "default", Models: "gpt-test"}
	require.NoError(t, db.Table(upgradeName).AutoMigrate(&channelUserGroupsMigrationLegacy{}))
	require.NoError(t, db.Table(upgradeName).Create(&legacy).Error)
	require.NoError(t, db.Table(upgradeName).AutoMigrate(&Channel{}))
	var migrated Channel
	require.NoError(t, db.Table(upgradeName).First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.Name, migrated.Name)
	assert.Equal(t, legacy.Key, migrated.Key)
	assert.Equal(t, legacy.Group, migrated.Group)
	assert.Equal(t, legacy.Models, migrated.Models)
	assert.Nil(t, migrated.UserGroups)
	assertUserGroupsColumn(t, db, upgradeName, true)
	assert.True(t, db.Migrator().HasIndex(upgradeName, db.NamingStrategy.IndexName(upgradeName, "name")))
	recorder.reset()
	require.NoError(t, db.Table(upgradeName).AutoMigrate(&Channel{}))
	assert.Empty(t, recorder.schemaMutations(), "a repeated upgrade migration must not repeat schema DDL")
}

func assertUserGroupsColumn(t *testing.T, db *gorm.DB, tableName string, wantNullable bool) {
	t.Helper()
	if db.Dialector.Name() == "sqlite" {
		type sqliteColumn struct {
			Name    string `gorm:"column:name"`
			NotNull int    `gorm:"column:notnull"`
			Type    string `gorm:"column:type"`
		}
		var columns []sqliteColumn
		require.Regexp(t, `^[A-Za-z0-9_]+$`, tableName)
		require.NoError(t, db.Raw("PRAGMA table_info("+tableName+")").Scan(&columns).Error)
		for _, column := range columns {
			if !strings.EqualFold(column.Name, "user_groups") {
				continue
			}
			assert.Equal(t, 0, column.NotNull)
			assert.Contains(t, strings.ToLower(column.Type), "varchar")
			return
		}
		t.Fatal("migrated schema does not contain user_groups")
	}

	columns, err := db.Table(tableName).Migrator().ColumnTypes(&Channel{})
	require.NoError(t, err)
	for _, column := range columns {
		if strings.EqualFold(column.Name(), "user_groups") {
			nullable, ok := column.Nullable()
			require.True(t, ok, "database driver must report user_groups nullability")
			assert.Equal(t, wantNullable, nullable)
			assert.Contains(t, strings.ToLower(column.DatabaseTypeName()), "varchar")
			length, ok := column.Length()
			if ok {
				assert.EqualValues(t, 255, length)
			}
			return
		}
	}
	t.Fatal("migrated schema does not contain user_groups")
}

func TestChannelUserGroupsMigrationSQLite(t *testing.T) {
	recorder := &migrationSQLRecorder{}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: recorder})
	require.NoError(t, err)
	// Every new physical connection to ":memory:" gets its own isolated
	// database, so the pool must stay at one connection for the writes and
	// reads below to observe the same schema.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	testChannelUserGroupsMigration(t, db, recorder)
}

func TestChannelUserGroupsMigrationConfiguredDatabases(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		dialector func(string) gorm.Dialector
	}{
		{name: "mysql", env: "TEST_MYSQL_DSN", dialector: func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
		{name: "postgres", env: "TEST_POSTGRES_DSN", dialector: func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(test.env))
			if dsn == "" {
				t.Skip(test.env + " is not configured")
			}
			recorder := &migrationSQLRecorder{}
			db, err := gorm.Open(test.dialector(dsn), &gorm.Config{Logger: recorder})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			testChannelUserGroupsMigration(t, db, recorder)
		})
	}
}
