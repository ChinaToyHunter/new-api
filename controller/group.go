package controller

import (
	"net/http"
	"sort"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func writeGroupCatalog(c *gin.Context, groupNames []string) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    groupNames,
	})
}

// GetGroups keeps the legacy admin route-group catalog endpoint.
func GetGroups(c *gin.Context) {
	GetRouteGroups(c)
}

func GetAccountGroups(c *gin.Context) {
	writeGroupCatalog(c, setting.GetSortedAccountGroupIDs())
}

func GetRouteGroups(c *gin.Context) {
	groupRatios := ratio_setting.GetGroupRatioCopy()
	groupNames := make([]string, 0, len(groupRatios))
	for groupName := range groupRatios {
		groupNames = append(groupNames, groupName)
	}
	sort.Strings(groupNames)
	writeGroupCatalog(c, groupNames)
}

func GetUserGroups(c *gin.Context) {
	usableGroups := make(map[string]map[string]any)
	userGroup := ""
	userId := c.GetInt("id")
	userGroup, _ = model.GetUserGroup(userId, false)
	userUsableGroups := service.GetUserUsableGroups(userGroup)
	for groupName := range ratio_setting.GetGroupRatioCopy() {
		// UserUsableGroups contains the groups that the user can use
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = map[string]any{
				"ratio": service.GetUserGroupRatio(userGroup, groupName),
				"desc":  desc,
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		usableGroups["auto"] = map[string]any{
			"ratio": "自动",
			"desc":  setting.GetUsableGroupDescription("auto"),
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usableGroups,
	})
}
