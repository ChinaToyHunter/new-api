package controller

import (
	"errors"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/selfupdate"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func CheckSystemUpdate(c *gin.Context) {
	force := c.Query("force") == "true"
	info, err := selfupdate.Default().Check(c.Request.Context(), force)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, info)
}

func ListSystemUpdateReleases(c *gin.Context) {
	if !operation_setting.SystemUpdateTestModeEnabled {
		common.ApiErrorMsg(c, "system update test mode is disabled")
		return
	}
	releases, err := selfupdate.Default().ListReleases(c.Request.Context(), 50)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, releases)
}

type systemUpdateRequest struct {
	Version string `json:"version"`
}

func PerformSystemUpdate(c *gin.Context) {
	var request systemUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil && !errors.Is(err, io.EOF) {
		common.ApiErrorMsg(c, "invalid request body")
		return
	}
	request.Version = strings.TrimSpace(request.Version)
	if request.Version != "" && !operation_setting.SystemUpdateTestModeEnabled {
		common.ApiErrorMsg(c, "system update test mode is disabled")
		return
	}

	svc := selfupdate.Default()
	var result *selfupdate.PerformResult
	var err error
	if request.Version == "" {
		result, err = svc.Perform(c.Request.Context())
	} else {
		result, err = svc.PerformVersion(c.Request.Context(), request.Version)
	}
	if err != nil {
		if errors.Is(err, selfupdate.ErrUpdateInProgress) {
			common.ApiErrorMsg(c, "update already in progress")
			return
		}
		if errors.Is(err, selfupdate.ErrUpdateDisabled) {
			common.ApiErrorMsg(c, "self-update is disabled")
			return
		}
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func GetSystemUpdateStatus(c *gin.Context) {
	status := selfupdate.Default().Status()
	common.ApiSuccess(c, status)
}

func RestartSystem(c *gin.Context) {
	if err := selfupdate.Default().Restart(c.Request.Context()); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"message": "restart scheduled"})
}
