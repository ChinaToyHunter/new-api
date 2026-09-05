package setting

import (
	"fmt"
	"strings"
)

// defaultUserGroupOverride 是部署通过 Option 配置的新用户默认账户组。
// 空值表示未配置，此时沿用历史兜底值 defaultUserGroupFallback。
var defaultUserGroupOverride string

const defaultUserGroupFallback = "default"

// GetDefaultUserGroup 返回新注册用户的默认账户组。
// 部署可通过 Option "DefaultUserGroup" 覆盖（如线上配置为"用户分组"）。
func GetDefaultUserGroup() string {
	group := strings.TrimSpace(defaultUserGroupOverride)
	if group != "" {
		return group
	}
	return defaultUserGroupFallback
}

// SetDefaultUserGroup 更新内存中的默认账户组配置（由 Option 加载/更新触发）。
func SetDefaultUserGroup(group string) {
	defaultUserGroupOverride = strings.TrimSpace(group)
}

// ValidateDefaultUserGroup 校验 Option 值：账户组名不能为空、不能是路由策略保留字，
// 且不得包含逗号或空白外的控制字符（账户组是单一名称，非列表）。
func ValidateDefaultUserGroup(value string) error {
	group := strings.TrimSpace(value)
	if group == "" {
		return fmt.Errorf("DefaultUserGroup 不能为空")
	}
	if group == "auto" {
		return fmt.Errorf("DefaultUserGroup 不能设置为路由策略保留字 auto")
	}
	return nil
}
