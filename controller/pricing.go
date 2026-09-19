package controller

import (
	"maps"

	"github.com/QuantumNous/new-api/common"
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
		if common.StringsContains(item.EnableGroup, "all") {
			filtered = append(filtered, item)
			continue
		}
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func GetPricing(c *gin.Context) {
	pricing := model.GetPricing()
	userId, exists := c.Get("id")
	usableGroup := map[string]string{}
	groupRatio := map[string]float64{}
	maps.Copy(groupRatio, ratio_setting.GetGroupRatioCopy())
	var accountGroup string
	if exists {
		user, err := model.GetUserCache(userId.(int))
		if err == nil {
			accountGroup = user.Group
			for routeGroup := range groupRatio {
				ratio, ok := ratio_setting.GetGroupGroupRatio(accountGroup, routeGroup)
				if ok {
					groupRatio[routeGroup] = ratio
				}
			}
		}
	}

	usableGroup = service.GetUserUsableGroups(accountGroup)
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	for routeGroup := range ratio_setting.GetGroupRatioCopy() {
		if _, ok := usableGroup[routeGroup]; !ok {
			delete(groupRatio, routeGroup)
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
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "重置模型倍率成功",
	})
}
