// Package webtools runs the hosted web search and page fetch tools that the
// gateway answers itself when the selected upstream cannot run them. Calls go
// to the DeepSeek Search and Fetch APIs through the channels that serve the
// deepseek-search and deepseek-fetch models, so their keys, priorities,
// weights and automatic disabling apply as for any other relay.
package webtools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// Tool kinds, which are also the tool-price keys of gateway calls.
const (
	ToolWebSearch = dto.BuildInToolWebSearch
	ToolWebFetch  = "web_fetch"
)

// Error codes of failed calls, named as Claude's web search and web fetch
// tool results name them.
const (
	ErrorInvalidInput     = "invalid_input"
	ErrorUnavailable      = "unavailable"
	ErrorTooManyRequests  = "too_many_requests"
	ErrorMaxUsesExceeded  = "max_uses_exceeded"
	ErrorURLNotAllowed    = "url_not_allowed"
	ErrorURLNotAccessible = "url_not_accessible"
	ErrorURLTooLong       = "url_too_long"
)

const (
	searchPath = "/v1/tools/search"
	fetchPath  = "/v1/tools/fetch"
	// maxAttempts bounds the channels and keys one call tries.
	maxAttempts      = 3
	callTimeout      = 30 * time.Second
	maxResponseBytes = 8 << 20
	// maxQueryRunes is DeepSeek's limit for a search query, operators included.
	maxQueryRunes = 128
	maxURLLength  = 8192

	backendContextKey = "web_tools_backend"
)

// ToolError is a failed call the model receives as an error result.
type ToolError struct {
	Code string
}

func (e *ToolError) Error() string {
	return "web tool call failed: " + e.Code
}

// SearchHit is one web search result.
type SearchHit struct {
	Title   string
	URL     string
	Summary string
	Date    string
}

// Page is the readable content of a fetched web page.
type Page struct {
	Title   string
	Content string
}

// Backend runs the gateway's web tool calls.
type Backend interface {
	// Serves reports whether any channel can run calls of the tool kind.
	Serves(tool string) bool
	Search(c *gin.Context, query string, count int) ([]SearchHit, error)
	Fetch(c *gin.Context, pageURL string) (*Page, error)
}

// GatewayMode reports whether the gateway runs the hosted web tools of a
// request on its selected channel instead of forwarding them upstream. A
// channel's own mode wins; otherwise requests matched by one of the native
// channel affinity rules (the Claude Code and Codex CLI traces) keep the
// upstream's tools, and the rest follow the global default.
func GatewayMode(c *gin.Context, info *relaycommon.RelayInfo) bool {
	setting := operation_setting.GetWebToolsSetting()
	if !setting.Enabled || info == nil || info.ChannelMeta == nil {
		return false
	}
	// A passthrough body reaches the upstream unchanged, tools included.
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		return false
	}
	switch info.ChannelOtherSettings.WebToolsMode {
	case operation_setting.WebToolsModeNative:
		return false
	case operation_setting.WebToolsModeGateway:
		return true
	}
	if rule := service.RequestPolicy(c).RuleName; rule != "" && slices.Contains(setting.NativeAffinityRules, rule) {
		return false
	}
	return setting.DefaultMode == operation_setting.WebToolsModeGateway
}

// NewBackend returns the gateway web tools backend of the request. Successful
// results are kept for the whole request, so a relay retry on another model
// channel does not pay for the same call again.
func NewBackend(c *gin.Context, info *relaycommon.RelayInfo) Backend {
	group := operation_setting.GetWebToolsSetting().ToolGroup
	if group == "" {
		group = info.UsingGroup
		if autoGroup := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); group == "auto" && autoGroup != "" {
			group = autoGroup
		}
	}
	if existing, ok := c.Get(backendContextKey); ok {
		if backend, ok := existing.(*deepSeekBackend); ok && backend.group == group {
			return backend
		}
	}
	backend := &deepSeekBackend{
		group: group,
		pick: func(group string, modelName string, retry int) (*model.Channel, error) {
			return model.GetRandomSatisfiedChannel(group, modelName, retry, nil)
		},
		results: make(map[string]any),
	}
	c.Set(backendContextKey, backend)
	return backend
}

type deepSeekBackend struct {
	group   string
	pick    func(group string, modelName string, retry int) (*model.Channel, error)
	mu      sync.Mutex
	results map[string]any
}

func (b *deepSeekBackend) Serves(tool string) bool {
	modelName := constant.DeepSeekSearchModel
	if tool == ToolWebFetch {
		modelName = constant.DeepSeekFetchModel
	}
	channel, err := b.pick(b.group, modelName, 0)
	return err == nil && channel != nil
}

func (b *deepSeekBackend) Search(c *gin.Context, query string, count int) ([]SearchHit, error) {
	cacheKey := fmt.Sprintf("search\x00%d\x00%s", count, query)
	if hits, ok := b.cached(cacheKey).([]SearchHit); ok {
		return hits, nil
	}
	body, err := common.Marshal(map[string]any{"query": query, "count": count})
	if err != nil {
		return nil, &ToolError{Code: ErrorInvalidInput}
	}
	var response struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Snippet string  `json:"snippet"`
			Summary string  `json:"summary"`
			Date    *string `json:"date"`
		} `json:"results"`
	}
	if err := b.call(c, constant.DeepSeekSearchModel, searchPath, body, &response); err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(response.Results))
	for _, result := range response.Results {
		if result.URL == "" {
			continue
		}
		hit := SearchHit{Title: result.Title, URL: result.URL, Summary: result.Summary}
		if hit.Summary == "" {
			hit.Summary = result.Snippet
		}
		if result.Date != nil {
			hit.Date = *result.Date
		}
		hits = append(hits, hit)
	}
	b.store(cacheKey, hits)
	return hits, nil
}

func (b *deepSeekBackend) Fetch(c *gin.Context, pageURL string) (*Page, error) {
	cacheKey := "fetch\x00" + pageURL
	if page, ok := b.cached(cacheKey).(*Page); ok {
		return page, nil
	}
	body, err := common.Marshal(map[string]string{"url": pageURL})
	if err != nil {
		return nil, &ToolError{Code: ErrorInvalidInput}
	}
	var response struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := b.call(c, constant.DeepSeekFetchModel, fetchPath, body, &response); err != nil {
		return nil, err
	}
	page := &Page{Title: response.Title, Content: response.Content}
	if page.Content != "" {
		b.store(cacheKey, page)
	}
	return page, nil
}

func (b *deepSeekBackend) cached(key string) any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.results[key]
}

func (b *deepSeekBackend) store(key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.results[key] = value
}

// call posts one Search or Fetch request, trying further channels and keys
// when a channel is unavailable, rate limited or rejects its key. Errors about
// the request itself or the fetched page end the call.
func (b *deepSeekBackend) call(c *gin.Context, modelName string, path string, body []byte, out any) error {
	failure := &ToolError{Code: ErrorUnavailable}
	for attempt := range maxAttempts {
		if c.Request.Context().Err() != nil {
			return failure
		}
		channel, err := b.pick(b.group, modelName, attempt)
		if err != nil || channel == nil {
			if attempt == 0 {
				logger.LogWarn(c, "web tools: no channel serves %s in group %s", modelName, b.group)
			}
			return failure
		}
		switch channel.Type {
		case constant.ChannelTypeDeepSeek, constant.ChannelTypeNewAPI, constant.ChannelTypeSub2API:
		default:
			logger.LogWarn(c, "web tools: channel #%d cannot serve %s (type %d)", channel.Id, modelName, channel.Type)
			continue
		}
		key, _, keyErr := channel.GetNextEnabledKey()
		if keyErr != nil {
			continue
		}
		status, responseBody, err := b.post(c, channel, key, path, body)
		if err != nil {
			logger.LogWarn(c, "web tools: %s via channel #%d failed: %s", modelName, channel.Id, err.Error())
			continue
		}
		if status == http.StatusOK {
			if err := common.Unmarshal(responseBody, out); err != nil {
				logger.LogWarn(c, "web tools: %s via channel #%d returned an invalid body: %s", modelName, channel.Id, err.Error())
				continue
			}
			return nil
		}
		logger.LogWarn(c, "web tools: %s via channel #%d returned status %d", modelName, channel.Id, status)
		switch {
		case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge:
			return &ToolError{Code: ErrorInvalidInput}
		case path == fetchPath && status == http.StatusForbidden:
			// The Fetch security policy rejected the page, not the key.
			return &ToolError{Code: ErrorURLNotAllowed}
		case path == fetchPath && status == http.StatusNotFound:
			return &ToolError{Code: ErrorURLNotAccessible}
		case status == http.StatusTooManyRequests:
			failure = &ToolError{Code: ErrorTooManyRequests}
		default:
			failure = &ToolError{Code: ErrorUnavailable}
		}
		apiErr := service.RelayErrorHandler(c.Request.Context(), &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(responseBody))}, false)
		if service.ShouldDisableChannel(apiErr) {
			channelError := types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, key, channel.GetAutoBan())
			reason := apiErr.MaskSensitiveErrorWithStatusCode()
			gopool.Go(func() {
				service.DisableChannel(*channelError, reason)
			})
		}
	}
	return failure
}

func (b *deepSeekBackend) post(c *gin.Context, channel *model.Channel, key string, path string, body []byte) (int, []byte, error) {
	setting := channel.GetSetting()
	client, err := service.GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return 0, nil, err
	}
	// The shared client is copied so a redirect cannot carry the key to
	// another host.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), callTimeout)
	defer cancel()
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = constant.GetChannelBaseURL(channel.Type)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	// The channel's own header overrides apply. Client header passthrough
	// does not: the call is the gateway's, and a nil context rejects it.
	overrides, err := relaychannel.ResolveHeaderOverride(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ApiKey: key, HeadersOverride: channel.GetHeaderOverride()},
	}, nil)
	if err != nil {
		return 0, nil, err
	}
	for name, value := range overrides {
		request.Header.Set(name, value)
		if strings.EqualFold(name, "Host") {
			request.Host = value
		}
	}
	response, err := noRedirect.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if len(data) > maxResponseBytes {
		return 0, nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return response.StatusCode, data, nil
}

// SearchQuery adds the domain limits of a hosted search tool to the model's
// query as DeepSeek search operators, within DeepSeek's query length. Domains
// that do not fit are still enforced by FilterHits.
func SearchQuery(query string, allowedDomains []string, blockedDomains []string) string {
	var operators strings.Builder
	for i, domain := range allowedDomains {
		if i > 0 {
			operators.WriteString(" OR")
		}
		operators.WriteString(" site:")
		operators.WriteString(domain)
	}
	for _, domain := range blockedDomains {
		operators.WriteString(" -site:")
		operators.WriteString(domain)
	}
	suffix := operators.String()
	if utf8.RuneCountInString(suffix) > maxQueryRunes/2 {
		suffix = ""
	}
	query = strings.TrimSpace(query)
	if limit := maxQueryRunes - utf8.RuneCountInString(suffix); utf8.RuneCountInString(query) > limit {
		query = string([]rune(query)[:limit])
	}
	return query + suffix
}

// FilterHits drops results outside the domain limits of a hosted search tool.
func FilterHits(hits []SearchHit, allowedDomains []string, blockedDomains []string) []SearchHit {
	if len(allowedDomains) == 0 && len(blockedDomains) == 0 {
		return hits
	}
	filtered := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		if parsed, err := url.Parse(hit.URL); err == nil && domainAllowed(parsed.Hostname(), allowedDomains, blockedDomains) {
			filtered = append(filtered, hit)
		}
	}
	return filtered
}

// CheckURL validates a page URL against DeepSeek's Fetch limits and the
// domain limits of a hosted fetch tool.
func CheckURL(pageURL string, allowedDomains []string, blockedDomains []string) error {
	if pageURL == "" {
		return &ToolError{Code: ErrorInvalidInput}
	}
	if len(pageURL) > maxURLLength {
		return &ToolError{Code: ErrorURLTooLong}
	}
	parsed, err := url.Parse(pageURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return &ToolError{Code: ErrorInvalidInput}
	}
	if parsed.User != nil || !domainAllowed(parsed.Hostname(), allowedDomains, blockedDomains) {
		return &ToolError{Code: ErrorURLNotAllowed}
	}
	return nil
}

func domainAllowed(host string, allowedDomains []string, blockedDomains []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	matches := func(domains []string) bool {
		for _, domain := range domains {
			domain = strings.ToLower(strings.Trim(strings.TrimSpace(domain), "."))
			if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
				return true
			}
		}
		return false
	}
	if len(allowedDomains) > 0 && !matches(allowedDomains) {
		return false
	}
	return !matches(blockedDomains)
}
