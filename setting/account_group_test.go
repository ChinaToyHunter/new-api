package setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func restoreAccountGroupSettings(t *testing.T) {
	t.Helper()
	originalGroups := AccountGroups2JSONString()
	originalDefault := GetDefaultUserGroup()
	t.Cleanup(func() {
		require.NoError(t, UpdateAccountGroupSettingsByJSONString(originalGroups, originalDefault))
	})
}

func TestAccountGroupsRemainIndependentFromRouteGroups(t *testing.T) {
	restoreAccountGroupSettings(t)

	require.NoError(t, UpdateAccountGroupSettingsByJSONString(
		`{"default":"Default account","会员":"Member account"}`,
		"会员",
	))

	assert.Equal(t, []string{"default", "会员"}, GetSortedAccountGroupIDs())
	assert.True(t, ContainsAccountGroup("会员"))
	assert.False(t, ContainsAccountGroup("auto"))
	assert.Equal(t, "会员", GetDefaultUserGroup())
}

func TestAccountGroupSettingsRejectInvalidDefaultAndReservedIDs(t *testing.T) {
	restoreAccountGroupSettings(t)

	assert.Error(t, ValidateAccountGroupSettingsJSON(`{"default":"Default"}`, "missing"))
	assert.Error(t, ValidateAccountGroupSettingsJSON(`{"auto":"Reserved"}`, "auto"))
	assert.Error(t, ValidateAccountGroupSettingsJSON(`{"bad,group":"Invalid"}`, "bad,group"))
	assert.Error(t, ValidateAccountGroupSettingsJSON("{\"badgroup\":\"Invalid\"}", "badgroup"))
}

func TestLoadAccountGroupOptionsAddsLegacyCustomDefaultOnlyWithoutCanonicalCatalog(t *testing.T) {
	restoreAccountGroupSettings(t)

	require.NoError(t, LoadAccountGroupOptions("", "legacy-account", false))
	assert.True(t, ContainsAccountGroup("legacy-account"))
	assert.Equal(t, "legacy-account", GetDefaultUserGroup())

	err := LoadAccountGroupOptions(`{"default":"Default"}`, "legacy-account", true)
	assert.Error(t, err)
	assert.True(t, ContainsAccountGroup("legacy-account"))
	assert.Equal(t, "legacy-account", GetDefaultUserGroup())
}
