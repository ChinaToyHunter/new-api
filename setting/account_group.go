package setting

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/QuantumNous/new-api/common"
)

const (
	AccountGroupsOptionKey = "AccountGroups"
	GroupContractVersion   = 2
)

var defaultAccountGroups = map[string]string{
	"default": "默认分组",
	"vip":     "vip分组",
	"svip":    "svip分组",
	"用户分组":    "用户分组",
}

var accountGroups = cloneAccountGroups(defaultAccountGroups)
var accountGroupSettingsMutex sync.RWMutex

func cloneAccountGroups(groups map[string]string) map[string]string {
	result := make(map[string]string, len(groups))
	for id, description := range groups {
		result[id] = description
	}
	return result
}

func validateAccountGroupID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("账户组 ID 不能为空")
	}
	if id != strings.TrimSpace(id) {
		return fmt.Errorf("账户组 ID 不能包含首尾空白: %q", id)
	}
	if id == "auto" {
		return fmt.Errorf("账户组 ID 不能使用路由策略保留字 auto")
	}
	if strings.Contains(id, ",") {
		return fmt.Errorf("账户组 ID 不能包含逗号: %q", id)
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return fmt.Errorf("账户组 ID 不能包含控制字符: %q", id)
		}
	}
	return nil
}

func parseAccountGroups(jsonStr string) (map[string]string, error) {
	var groups map[string]string
	if err := common.UnmarshalJsonStr(jsonStr, &groups); err != nil {
		return nil, err
	}
	if groups == nil {
		return nil, fmt.Errorf("AccountGroups 必须是 JSON 对象")
	}
	for id := range groups {
		if err := validateAccountGroupID(id); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

func validateAccountGroupSettings(groups map[string]string, defaultGroup string) error {
	if err := validateAccountGroupID(defaultGroup); err != nil {
		return err
	}
	if _, ok := groups[defaultGroup]; !ok {
		return fmt.Errorf("AccountGroups 必须包含 DefaultUserGroup %q", defaultGroup)
	}
	return nil
}

func replaceAccountGroupSettings(groups map[string]string, defaultGroup string) {
	accountGroupSettingsMutex.Lock()
	accountGroups = cloneAccountGroups(groups)
	defaultUserGroupOverride = defaultGroup
	accountGroupSettingsMutex.Unlock()
}

// LoadAccountGroupOptions initializes the related options as one consistent state.
// An installation without AccountGroups keeps the built-in compatibility catalog
// and adds a custom persisted DefaultUserGroup without importing route groups.
func LoadAccountGroupOptions(accountGroupsJSON, defaultGroup string, hasCanonicalOption bool) error {
	defaultGroup = strings.TrimSpace(defaultGroup)
	if defaultGroup == "" {
		defaultGroup = defaultUserGroupFallback
	}
	groups := cloneAccountGroups(defaultAccountGroups)
	if hasCanonicalOption {
		parsed, err := parseAccountGroups(accountGroupsJSON)
		if err != nil {
			return err
		}
		groups = parsed
	} else if _, ok := groups[defaultGroup]; !ok {
		groups[defaultGroup] = defaultGroup
	}
	if err := validateAccountGroupSettings(groups, defaultGroup); err != nil {
		return err
	}
	replaceAccountGroupSettings(groups, defaultGroup)
	return nil
}

func ValidateAccountGroupSettingsJSON(accountGroupsJSON, defaultGroup string) error {
	groups, err := parseAccountGroups(accountGroupsJSON)
	if err != nil {
		return err
	}
	return validateAccountGroupSettings(groups, strings.TrimSpace(defaultGroup))
}

func UpdateAccountGroupSettingsByJSONString(accountGroupsJSON, defaultGroup string) error {
	groups, err := parseAccountGroups(accountGroupsJSON)
	if err != nil {
		return err
	}
	defaultGroup = strings.TrimSpace(defaultGroup)
	if err := validateAccountGroupSettings(groups, defaultGroup); err != nil {
		return err
	}
	replaceAccountGroupSettings(groups, defaultGroup)
	return nil
}

func GetAccountGroupsCopy() map[string]string {
	accountGroupSettingsMutex.RLock()
	defer accountGroupSettingsMutex.RUnlock()
	return cloneAccountGroups(accountGroups)
}

func ContainsAccountGroup(id string) bool {
	accountGroupSettingsMutex.RLock()
	defer accountGroupSettingsMutex.RUnlock()
	_, ok := accountGroups[strings.TrimSpace(id)]
	return ok
}

func GetSortedAccountGroupIDs() []string {
	groups := GetAccountGroupsCopy()
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func AccountGroups2JSONString() string {
	encoded, err := common.Marshal(GetAccountGroupsCopy())
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func ValidateAccountGroupsJSON(jsonStr string) error {
	return ValidateAccountGroupSettingsJSON(jsonStr, GetDefaultUserGroup())
}

func UpdateAccountGroupsByJSONString(jsonStr string) error {
	return UpdateAccountGroupSettingsByJSONString(jsonStr, GetDefaultUserGroup())
}
