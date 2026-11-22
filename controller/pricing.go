package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		// The pricing cache is shared by all viewers.
		groups := make([]string, 0, len(item.EnableGroup))
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok || group == "all" {
				groups = append(groups, group)
			}
		}
		if len(groups) > 0 {
			item.EnableGroup = groups
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func GetPricing(c *gin.Context) {
	accountGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	usableGroup := service.GetUserUsableGroups(accountGroup)
	pricing := filterPricingByUsableGroups(model.GetPricing(), usableGroup)
	groupRatio := ratio_setting.GetGroupRatioCopy()
	for routeGroup := range groupRatio {
		if _, ok := usableGroup[routeGroup]; !ok {
			delete(groupRatio, routeGroup)
			continue
		}
		if accountGroup != "" {
			if ratio, ok := ratio_setting.GetGroupGroupRatio(accountGroup, routeGroup); ok {
				groupRatio[routeGroup] = ratio
			}
		}
	}
	autoRouteGroups := service.GetUserAutoGroup(accountGroup)

	c.JSON(200, gin.H{
		"success":                true,
		"data":                   pricing,
		"vendors":                model.GetVendors(),
		"group_ratio":            groupRatio,
		"usable_group":           usableGroup,
		"supported_endpoint":     model.GetSupportedEndpointMap(),
		"auto_groups":            autoRouteGroups,
		"pricing_version":        "a42d372ccf0b5dd13ecf71203521f9d2",
		"account_group":          accountGroup,
		"route_group_ratio":      groupRatio,
		"usable_route_groups":    usableGroup,
		"auto_route_groups":      autoRouteGroups,
		"group_contract_version": setting.GroupContractVersion,
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Model ratios reset", nil)
}
