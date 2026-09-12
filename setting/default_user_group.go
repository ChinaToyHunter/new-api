package setting

import "strings"

// defaultUserGroupOverride 是部署通过 Option 配置的新用户默认账户组。
// 空值表示未配置，此时沿用历史兜底值 defaultUserGroupFallback。
var defaultUserGroupOverride string

const defaultUserGroupFallback = "default"

// GetDefaultUserGroup 返回新注册用户的默认账户组。
// 部署可通过 Option "DefaultUserGroup" 覆盖（如线上配置为"用户分组"）。
func GetDefaultUserGroup() string {
	accountGroupSettingsMutex.RLock()
	defer accountGroupSettingsMutex.RUnlock()
	group := strings.TrimSpace(defaultUserGroupOverride)
	if group != "" {
		return group
	}
	return defaultUserGroupFallback
}

// SetDefaultUserGroup 更新内存中的默认账户组配置（由 Option 加载/更新触发）。
func SetDefaultUserGroup(group string) {
	accountGroupSettingsMutex.Lock()
	defaultUserGroupOverride = strings.TrimSpace(group)
	accountGroupSettingsMutex.Unlock()
}

// ValidateDefaultUserGroup 校验账户组 ID，并确保它存在于独立账户组目录。
func ValidateDefaultUserGroup(value string) error {
	group := strings.TrimSpace(value)
	if err := validateAccountGroupID(group); err != nil {
		return err
	}
	if !ContainsAccountGroup(group) {
		return &UnknownAccountGroupError{Group: group}
	}
	return nil
}

type UnknownAccountGroupError struct {
	Group string
}

func (e *UnknownAccountGroupError) Error() string {
	return "DefaultUserGroup 不存在于 AccountGroups: " + e.Group
}
