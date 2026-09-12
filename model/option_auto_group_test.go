package model

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestValidateOptionValueRejectsInvalidPaymentFees(t *testing.T) {
	for _, value := range []string{"", "-1", "NaN", "+Inf", "invalid"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue(operation_setting.EpayFeePercentOptionKey, value))
		})
	}
	require.NoError(t, validateOptionValue(operation_setting.WaffoFeeFixedOptionKey, "0.25"))
}

func TestValidateOptionValueRejectsAccountCatalogWithoutDefault(t *testing.T) {
	originalGroups := setting.AccountGroups2JSONString()
	originalDefault := setting.GetDefaultUserGroup()
	require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(`{"default":"Default"}`, "default"))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(originalGroups, originalDefault))
	})

	assert.Error(t, validateOptionValue(setting.AccountGroupsOptionKey, `{"vip":"VIP"}`))
	require.NoError(t, validateOptionValue(setting.AccountGroupsOptionKey, `{"default":"Default","vip":"VIP"}`))
	assert.Error(t, validateOptionValue("DefaultUserGroup", "vip"))
}

func TestValidateOptionsAcceptsAtomicAccountCatalogAndDefaultChange(t *testing.T) {
	originalGroups := setting.AccountGroups2JSONString()
	originalDefault := setting.GetDefaultUserGroup()
	require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(`{"default":"Default"}`, "default"))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(originalGroups, originalDefault))
	})

	require.NoError(t, validateOptions(map[string]string{
		setting.AccountGroupsOptionKey: `{"vip":"VIP"}`,
		"DefaultUserGroup":             "vip",
	}))
	assert.Error(t, validateOptions(map[string]string{
		setting.AccountGroupsOptionKey: `{"vip":"VIP"}`,
	}))
}

func TestLoadOptionsFromDatabaseRejectsInvalidAccountGroupPairAtomically(t *testing.T) {
	previousDB := DB
	previousOptionMap := common.OptionMap
	originalGroups := setting.AccountGroups2JSONString()
	originalDefault := setting.GetDefaultUserGroup()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Option{}))
	DB = database
	common.OptionMap = map[string]string{
		setting.AccountGroupsOptionKey: originalGroups,
		"DefaultUserGroup":             originalDefault,
	}
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptionMap
		require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(originalGroups, originalDefault))
	})

	require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(`{"stable":"Stable"}`, "stable"))
	common.OptionMap[setting.AccountGroupsOptionKey] = `{"stable":"Stable"}`
	common.OptionMap["DefaultUserGroup"] = "stable"
	require.NoError(t, database.Create(&[]Option{
		{Key: setting.AccountGroupsOptionKey, Value: `{"vip":"VIP"}`},
		{Key: "DefaultUserGroup", Value: "missing"},
	}).Error)

	loadOptionsFromDatabase()

	assert.Equal(t, []string{"stable"}, setting.GetSortedAccountGroupIDs())
	assert.Equal(t, "stable", setting.GetDefaultUserGroup())
	assert.JSONEq(t, `{"stable":"Stable"}`, common.OptionMap[setting.AccountGroupsOptionKey])
	assert.Equal(t, "stable", common.OptionMap["DefaultUserGroup"])
}

func TestUpdateOptionsBulkAccountGroupPairConfiguredDatabases(t *testing.T) {
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

			database, err := gorm.Open(test.dialector(dsn), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := database.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

			tableName := fmt.Sprintf("option_group_split_%s", test.name)
			require.NoError(t, database.Table(tableName).AutoMigrate(&Option{}))
			t.Cleanup(func() {
				require.NoError(t, database.Migrator().DropTable(tableName))
			})

			previousDB := DB
			previousOptionMap := common.OptionMap
			originalGroups := setting.AccountGroups2JSONString()
			originalDefault := setting.GetDefaultUserGroup()
			DB = database.Table(tableName)
			common.OptionMap = map[string]string{}
			t.Cleanup(func() {
				DB = previousDB
				common.OptionMap = previousOptionMap
				require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(originalGroups, originalDefault))
			})

			require.NoError(t, setting.UpdateAccountGroupSettingsByJSONString(`{"default":"Default"}`, "default"))
			require.NoError(t, UpdateOptionsBulk(map[string]string{
				setting.AccountGroupsOptionKey: `{"vip":"VIP"}`,
				"DefaultUserGroup":             "vip",
			}))

			var options []Option
			require.NoError(t, database.Table(tableName).Find(&options).Error)
			require.Len(t, options, 2)
			values := make(map[string]string, len(options))
			for _, option := range options {
				values[option.Key] = option.Value
			}
			assert.JSONEq(t, `{"vip":"VIP"}`, values[setting.AccountGroupsOptionKey])
			assert.Equal(t, "vip", values["DefaultUserGroup"])
			assert.Equal(t, []string{"vip"}, setting.GetSortedAccountGroupIDs())
			assert.Equal(t, "vip", setting.GetDefaultUserGroup())
		})
	}
}

func TestValidateOptionValueRejectsInvalidMaxTokenAutoGroups(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue("MaxTokenAutoGroups", value))
		})
	}
	require.NoError(t, validateOptionValue("MaxTokenAutoGroups", "999999"))
}
