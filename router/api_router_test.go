/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package router

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expectedApiRoutes are the api-router endpoints a wholesale one-sided resolve
// of an upstream sync once dropped while leaving every handler in place, so the
// web UI received "Invalid URL" instead of a response. Registering against a
// real engine is also the only guard against gin's wildcard-conflict panic.
func expectedApiRoutes() []string {
	return []string{
		// Restored after the sync dropped them.
		http.MethodPost + " /api/oauth/email/bind/start",
		http.MethodPost + " /api/oauth/email/bind/resend",
		http.MethodGet + " /api/verify/methods",
		http.MethodPost + " /api/user/login/verify",
		http.MethodPost + " /api/user/login/passkey/begin",
		http.MethodPost + " /api/user/login/passkey/finish",
		http.MethodPut + " /api/option/passkey/domains",
		http.MethodGet + " /api/option/model_pricing",
		http.MethodPatch + " /api/option/model_pricing",
		http.MethodPost + " /api/option/model_pricing/convert",
		http.MethodPost + " /api/option/model_pricing/preview",
		http.MethodGet + " /api/plugin/task/:key/icon",
		http.MethodPost + " /api/redemption/batch",
		http.MethodPost + " /api/vendors/operations/preview",
		http.MethodPost + " /api/vendors/operations",
		http.MethodPost + " /api/models/delete",

		// Fork-only, absent upstream: a sync must not drop these either.
		http.MethodPut + " /api/option/bulk",
		http.MethodPost + " /api/option/waffo-pancake/catalog",
		http.MethodGet + " /api/log/export",
		http.MethodGet + " /api/system/update/check",
		http.MethodGet + " /api/system/update/releases",
		http.MethodPost + " /api/system/update",
		http.MethodGet + " /api/system/update/status",
		http.MethodPost + " /api/system/restart",
		http.MethodGet + " /api/account-groups/",
		http.MethodGet + " /api/route-groups/",
	}
}

func TestSetApiRouterRegistersExpectedRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	require.NotPanics(t, func() { SetApiRouter(engine) })

	registered := make(map[string]struct{}, len(engine.Routes()))
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = struct{}{}
	}

	for _, route := range expectedApiRoutes() {
		_, ok := registered[route]
		assert.Truef(t, ok, "%s is not registered", route)
	}
}

// An unrouted path falls through to the fallback, which the browser reported as
// "Invalid URL". Every expected route must dispatch past that point instead.
func TestSetApiRouterDispatchesExpectedRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	unrouted := performPluginRequest(engine, http.MethodGet, "/api/option/not-a-route")
	require.Equal(t, http.StatusNotFound, unrouted.Code,
		"an unregistered path is expected to fall through to the fallback")

	for _, route := range expectedApiRoutes() {
		method, path, found := strings.Cut(route, " ")
		require.True(t, found)
		t.Run(route, func(t *testing.T) {
			response := performPluginRequest(engine, method, path)
			assert.NotEqual(t, http.StatusNotFound, response.Code)
		})
	}
}
