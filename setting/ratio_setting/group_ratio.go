package ratio_setting

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/types"
)

var defaultGroupRatio = map[string]float64{
	"default": 1,
	"vip":     1,
	"svip":    1,
}

var groupRatioMap = types.NewRWMap[string, float64]()

var defaultGroupGroupRatio = map[string]map[string]float64{
	"vip": {
		"edit_this": 0.9,
	},
}

var groupGroupRatioMap = types.NewRWMap[string, map[string]float64]()

var defaultGroupSpecialUsableGroup = map[string]map[string]string{}

type GroupRatioSetting struct {
	GroupRatio              *types.RWMap[string, float64]            `json:"group_ratio"`
	GroupGroupRatio         *types.RWMap[string, map[string]float64] `json:"group_group_ratio"`
	GroupSpecialUsableGroup *types.RWMap[string, map[string]string]  `json:"group_special_usable_group"`
}

var groupRatioSetting GroupRatioSetting

func init() {
	groupSpecialUsableGroup := types.NewRWMap[string, map[string]string]()
	groupSpecialUsableGroup.AddAll(defaultGroupSpecialUsableGroup)

	groupRatioMap.AddAll(defaultGroupRatio)
	groupGroupRatioMap.AddAll(defaultGroupGroupRatio)

	groupRatioSetting = GroupRatioSetting{
		GroupSpecialUsableGroup: groupSpecialUsableGroup,
		GroupRatio:              groupRatioMap,
		GroupGroupRatio:         groupGroupRatioMap,
	}

	config.GlobalConfig.Register("group_ratio_setting", &groupRatioSetting)
}

func GetGroupRatioSetting() *GroupRatioSetting {
	if groupRatioSetting.GroupSpecialUsableGroup == nil {
		groupRatioSetting.GroupSpecialUsableGroup = types.NewRWMap[string, map[string]string]()
		groupRatioSetting.GroupSpecialUsableGroup.AddAll(defaultGroupSpecialUsableGroup)
	}
	return &groupRatioSetting
}

func GetGroupRatioCopy() map[string]float64 {
	return groupRatioMap.ReadAll()
}

func ContainsGroupRatio(name string) bool {
	_, ok := groupRatioMap.Get(name)
	return ok
}

func GroupRatio2JSONString() string {
	return groupRatioMap.MarshalJSONString()
}

func UpdateGroupRatioByJSONString(jsonStr string) error {
	parsed := make(map[string]float64)
	if err := common.UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return err
	}
	if err := validateRatioMap(parsed, "group ratio"); err != nil {
		return err
	}
	groupRatioMap.ReplaceAll(parsed)
	return nil
}

func GetGroupRatio(name string) float64 {
	ratio, ok := groupRatioMap.Get(name)
	if !ok {
		common.SysLog("group ratio not found: " + name)
		return 1
	}
	return ratio
}

func GetGroupGroupRatio(userGroup, usingGroup string) (float64, bool) {
	gp, ok := groupGroupRatioMap.Get(userGroup)
	if !ok {
		return -1, false
	}
	ratio, ok := gp[usingGroup]
	if !ok {
		return -1, false
	}
	return ratio, true
}

func GroupGroupRatio2JSONString() string {
	return groupGroupRatioMap.MarshalJSONString()
}

func UpdateGroupGroupRatioByJSONString(jsonStr string) error {
	parsed, err := parseGroupGroupRatio(jsonStr)
	if err != nil {
		return err
	}
	groupGroupRatioMap.ReplaceAll(parsed)
	return nil
}

func CheckGroupGroupRatio(jsonStr string) error {
	_, err := parseGroupGroupRatio(jsonStr)
	return err
}

func parseGroupGroupRatio(jsonStr string) (map[string]map[string]float64, error) {
	parsed := make(map[string]map[string]float64)
	if err := common.UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return nil, err
	}
	for accountGroup, ratios := range parsed {
		for routeGroup, ratio := range ratios {
			if ratio < 0 {
				return nil, fmt.Errorf("group-group ratio must be not less than 0: %s/%s", accountGroup, routeGroup)
			}
		}
	}
	return parsed, nil
}

func CheckGroupRatio(jsonStr string) error {
	parsed := make(map[string]float64)
	if err := common.UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return err
	}
	return validateRatioMap(parsed, "group ratio")
}

func validateRatioMap(ratios map[string]float64, label string) error {
	for name, ratio := range ratios {
		if ratio < 0 {
			return fmt.Errorf("%s must be not less than 0: %s", label, name)
		}
	}
	return nil
}
