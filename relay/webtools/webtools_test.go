package webtools

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c
}

func newTestChannel(id int, baseURL string, key string) *model.Channel {
	return &model.Channel{Id: id, Name: "deepseek", Type: constant.ChannelTypeDeepSeek, Key: key, BaseURL: &baseURL}
}

func TestGatewayMode(t *testing.T) {
	setting := operation_setting.GetWebToolsSetting()
	saved := *setting
	t.Cleanup(func() { *setting = saved })

	tests := []struct {
		name        string
		enabled     bool
		defaultMode string
		channelMode string
		rule        string
		passThrough bool
		want        bool
	}{
		{name: "disabled", enabled: false, defaultMode: operation_setting.WebToolsModeGateway, want: false},
		{name: "default native", enabled: true, defaultMode: operation_setting.WebToolsModeNative, want: false},
		{name: "default gateway", enabled: true, defaultMode: operation_setting.WebToolsModeGateway, want: true},
		{name: "cli trace keeps native tools", enabled: true, defaultMode: operation_setting.WebToolsModeGateway, rule: "claude cli trace", want: false},
		{name: "other affinity rule follows default", enabled: true, defaultMode: operation_setting.WebToolsModeGateway, rule: "custom rule", want: true},
		{name: "channel gateway wins over cli trace", enabled: true, defaultMode: operation_setting.WebToolsModeNative, channelMode: "gateway", rule: "claude cli trace", want: true},
		{name: "channel native wins over default", enabled: true, defaultMode: operation_setting.WebToolsModeGateway, channelMode: "native", want: false},
		{name: "passthrough body is never rewritten", enabled: true, channelMode: "gateway", passThrough: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setting.Enabled = tt.enabled
			setting.DefaultMode = tt.defaultMode
			setting.NativeAffinityRules = []string{"codex cli trace", "claude cli trace"}
			c := newTestContext()
			service.RequestPolicy(c).RuleName = tt.rule
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				ChannelSetting:       dto.ChannelSettings{PassThroughBodyEnabled: tt.passThrough},
				ChannelOtherSettings: dto.ChannelOtherSettings{WebToolsMode: tt.channelMode},
			}}
			assert.Equal(t, tt.want, GatewayMode(c, info))
		})
	}
}

func TestDeepSeekBackendSearch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, searchPath, r.URL.Path)
		assert.Equal(t, "Bearer key-a", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"query":"go release","count":3}`, string(body))
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go","url":"https://go.dev/","snippet":"short","summary":"long","date":"2009-11-10T00:00:00+00:00"},
			{"title":"No URL","url":"","summary":"dropped"},
			{"title":"Blog","url":"https://go.dev/blog","snippet":"snippet only","date":null}]}`))
	}))
	defer server.Close()
	backend := &deepSeekBackend{group: "default", results: map[string]any{}, pick: func(group string, modelName string, retry int) (*model.Channel, error) {
		assert.Equal(t, "default", group)
		assert.Equal(t, constant.DeepSeekSearchModel, modelName)
		return newTestChannel(1, server.URL, "key-a"), nil
	}}

	hits, err := backend.Search(newTestContext(), "go release", 3)
	require.NoError(t, err)
	assert.Equal(t, []SearchHit{
		{Title: "Go", URL: "https://go.dev/", Summary: "long", Date: "2009-11-10T00:00:00+00:00"},
		{Title: "Blog", URL: "https://go.dev/blog", Summary: "snippet only"},
	}, hits)

	// A relay retry of the same request reuses the paid result.
	again, err := backend.Search(newTestContext(), "go release", 3)
	require.NoError(t, err)
	assert.Equal(t, hits, again)
	assert.EqualValues(t, 1, calls.Load())
}

func TestDeepSeekBackendTriesAnotherChannelWhenRateLimited(t *testing.T) {
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"QPS limit exceeded","type":"rate_limit_error","param":null,"code":"rate_limit"}}`))
	}))
	defer limited.Close()
	available := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer key-b", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"results":[{"title":"Go","url":"https://go.dev/","summary":"Go"}]}`))
	}))
	defer available.Close()
	channels := []*model.Channel{newTestChannel(1, limited.URL, "key-a"), newTestChannel(2, available.URL, "key-b")}
	backend := &deepSeekBackend{group: "default", results: map[string]any{}, pick: func(_ string, _ string, retry int) (*model.Channel, error) {
		return channels[min(retry, len(channels)-1)], nil
	}}

	hits, err := backend.Search(newTestContext(), "go", 5)
	require.NoError(t, err)
	assert.Len(t, hits, 1)
}

func TestDeepSeekBackendErrors(t *testing.T) {
	tests := []struct {
		name      string
		fetch     bool
		status    int
		wantCode  string
		wantCalls int32
	}{
		{name: "fetch policy rejection ends the call", fetch: true, status: http.StatusForbidden, wantCode: ErrorURLNotAllowed, wantCalls: 1},
		{name: "page without content ends the call", fetch: true, status: http.StatusNotFound, wantCode: ErrorURLNotAccessible, wantCalls: 1},
		{name: "invalid request ends the call", status: http.StatusBadRequest, wantCode: ErrorInvalidInput, wantCalls: 1},
		{name: "rejected key tries every attempt", status: http.StatusUnauthorized, wantCode: ErrorUnavailable, wantCalls: maxAttempts},
		{name: "rate limit tries every attempt", status: http.StatusTooManyRequests, wantCode: ErrorTooManyRequests, wantCalls: maxAttempts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":{"message":"failed","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`))
			}))
			defer server.Close()
			backend := &deepSeekBackend{group: "default", results: map[string]any{}, pick: func(string, string, int) (*model.Channel, error) {
				return newTestChannel(1, server.URL, "key-a"), nil
			}}

			var err error
			if tt.fetch {
				_, err = backend.Fetch(newTestContext(), "https://example.com/")
			} else {
				_, err = backend.Search(newTestContext(), "go", 5)
			}
			var toolErr *ToolError
			require.ErrorAs(t, err, &toolErr)
			assert.Equal(t, tt.wantCode, toolErr.Code)
			assert.Equal(t, tt.wantCalls, calls.Load())
		})
	}
}

func TestDeepSeekBackendWithoutChannel(t *testing.T) {
	backend := &deepSeekBackend{group: "default", results: map[string]any{}, pick: func(string, string, int) (*model.Channel, error) {
		return nil, nil
	}}
	assert.False(t, backend.Serves(ToolWebSearch))
	_, err := backend.Fetch(newTestContext(), "https://example.com/")
	var toolErr *ToolError
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, ErrorUnavailable, toolErr.Code)
}

func TestSearchQuery(t *testing.T) {
	assert.Equal(t, "go release", SearchQuery("  go release ", nil, nil))
	assert.Equal(t, "go site:go.dev OR site:github.com -site:example.com",
		SearchQuery("go", []string{"go.dev", "github.com"}, []string{"example.com"}))

	long := strings.Repeat("词", 200)
	query := SearchQuery(long, []string{"go.dev"}, nil)
	assert.Equal(t, maxQueryRunes, len([]rune(query)))
	assert.True(t, strings.HasSuffix(query, " site:go.dev"))

	many := strings.Split(strings.Repeat("a-very-long-domain-name.example.com,", 4), ",")
	assert.Equal(t, "go", SearchQuery("go", many[:4], nil), "operators that do not fit are left to FilterHits")
}

func TestDomainLimits(t *testing.T) {
	hits := []SearchHit{{URL: "https://go.dev/"}, {URL: "https://pkg.go.dev/x"}, {URL: "https://example.com/"}, {URL: "https://evilgo.dev/"}}
	assert.Equal(t, []SearchHit{{URL: "https://go.dev/"}, {URL: "https://pkg.go.dev/x"}}, FilterHits(hits, []string{"go.dev"}, nil))
	assert.Equal(t, []SearchHit{{URL: "https://go.dev/"}, {URL: "https://example.com/"}, {URL: "https://evilgo.dev/"}}, FilterHits(hits, nil, []string{"pkg.go.dev"}))

	tests := []struct {
		url  string
		code string
	}{
		{url: "https://go.dev/doc", code: ""},
		{url: "", code: ErrorInvalidInput},
		{url: "ftp://go.dev/", code: ErrorInvalidInput},
		{url: "https://user:secret@go.dev/", code: ErrorURLNotAllowed},
		{url: "https://example.com/", code: ErrorURLNotAllowed},
		{url: "https://go.dev/" + strings.Repeat("a", maxURLLength), code: ErrorURLTooLong},
	}
	for _, tt := range tests {
		err := CheckURL(tt.url, []string{"go.dev"}, nil)
		if tt.code == "" {
			assert.NoError(t, err, tt.url)
			continue
		}
		var toolErr *ToolError
		if assert.ErrorAs(t, err, &toolErr, tt.url) {
			assert.Equal(t, tt.code, toolErr.Code, tt.url)
		}
	}
}
