package modelroute

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type inboundRequest struct {
	path    string
	format  types.RelayFormat
	mode    int
	convert func(*Adaptor, *gin.Context, *relaycommon.RelayInfo, string) (any, error)
}

var (
	chatInbound = inboundRequest{
		path: "/v1/chat/completions", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions,
		convert: func(a *Adaptor, c *gin.Context, info *relaycommon.RelayInfo, model string) (any, error) {
			return a.ConvertOpenAIRequest(c, info, &dto.GeneralOpenAIRequest{Model: model, Messages: []dto.Message{{Role: "user", Content: "hi"}}})
		},
	}
	messagesInbound = inboundRequest{
		path: "/v1/messages", format: types.RelayFormatClaude, mode: relayconstant.RelayModeUnknown,
		convert: func(a *Adaptor, c *gin.Context, info *relaycommon.RelayInfo, model string) (any, error) {
			return a.ConvertClaudeRequest(c, info, &dto.ClaudeRequest{Model: model, Messages: []dto.ClaudeMessage{{Role: "user", Content: "hi"}}})
		},
	}
	responsesInbound = inboundRequest{
		path: "/v1/responses", format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses,
		convert: func(a *Adaptor, c *gin.Context, info *relaycommon.RelayInfo, model string) (any, error) {
			return a.ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{Model: model, Input: []byte(`"hi"`)})
		},
	}
)

// testChannelIDs gives every test relay its own channel so model catalogs are
// not shared between tests unless a test pins the ID.
var testChannelIDs atomic.Int32

func newTestRelay(t *testing.T, channelType int, baseURL string, inbound inboundRequest, model string) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, inbound.path, nil)
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		RelayFormat:     inbound.format,
		RelayMode:       inbound.mode,
		RequestURLPath:  inbound.path,
		OriginModelName: model,
		DisablePing:     true,
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       channelType,
			ChannelId:         int(testChannelIDs.Add(1)),
			ChannelBaseUrl:    baseURL,
			ApiKey:            "sk-test",
			UpstreamModelName: model,
		},
	}
	return c, recorder, info
}

// unavailableCatalog stands in for a Command Code host whose model list cannot
// be read, so routing falls back to the built-in rules without network access.
func unavailableCatalog(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	return server.URL + "/provider"
}

func TestAdaptorRoutesModelsToUpstreamProtocols(t *testing.T) {
	commandCodeBase := unavailableCatalog(t)
	tests := []struct {
		name        string
		channelType int
		baseURL     string
		rules       []dto.ModelProtocolRule
		inbound     inboundRequest
		model       string
		wantURL     string
		wantRequest any
	}{
		{"go chat model stays on chat", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", nil, chatInbound, "glm-5.3", "https://opencode.ai/zen/go/v1/chat/completions", &dto.GeneralOpenAIRequest{}},
		{"go messages to chat model", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", nil, messagesInbound, "glm-5.3", "https://opencode.ai/zen/go/v1/chat/completions", &dto.GeneralOpenAIRequest{}},
		{"go gpt model uses responses", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", nil, chatInbound, "gpt-5.6-luna", "https://opencode.ai/zen/go/v1/responses", dto.OpenAIResponsesRequest{}},
		{"go prefixed minimax uses messages", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", nil, chatInbound, "opencode-go/MiniMax-M3", "https://opencode.ai/zen/go/v1/messages", &dto.ClaudeRequest{}},
		{"go responses to minimax", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", nil, responsesInbound, "minimax-m3", "https://opencode.ai/zen/go/v1/messages", &dto.ClaudeRequest{}},
		{"zen qwen3.8-max uses chat", constant.ChannelTypeOpenCodeZen, "https://opencode.ai/zen", nil, messagesInbound, "qwen3.8-max", "https://opencode.ai/zen/v1/chat/completions", &dto.GeneralOpenAIRequest{}},
		{"zen other qwen uses messages", constant.ChannelTypeOpenCodeZen, "https://opencode.ai/zen", nil, chatInbound, "qwen3.7-plus", "https://opencode.ai/zen/v1/messages", &dto.ClaudeRequest{}},
		{"zen minimax uses chat", constant.ChannelTypeOpenCodeZen, "https://opencode.ai/zen", nil, messagesInbound, "minimax-m3", "https://opencode.ai/zen/v1/chat/completions", &dto.GeneralOpenAIRequest{}},
		{"zen claude from responses", constant.ChannelTypeOpenCodeZen, "https://opencode.ai/zen", nil, responsesInbound, "claude-sonnet-4-6", "https://opencode.ai/zen/v1/messages", &dto.ClaudeRequest{}},
		{"base url with v1 is not doubled", constant.ChannelTypeOpenCodeZen, "https://opencode.ai/zen/v1/", nil, chatInbound, "gpt-5.6", "https://opencode.ai/zen/v1/responses", dto.OpenAIResponsesRequest{}},
		{"command code gpt keeps chat", constant.ChannelTypeCommandCode, commandCodeBase, nil, chatInbound, "gpt-5.5", commandCodeBase + "/v1/chat/completions", &dto.GeneralOpenAIRequest{}},
		{"command code gpt from messages", constant.ChannelTypeCommandCode, commandCodeBase, nil, messagesInbound, "gpt-5.5", commandCodeBase + "/v1/responses", dto.OpenAIResponsesRequest{}},
		{"command code claude from chat", constant.ChannelTypeCommandCode, commandCodeBase, nil, chatInbound, "claude-sonnet-4-6", commandCodeBase + "/v1/messages", &dto.ClaudeRequest{}},
		{"command code other model from responses", constant.ChannelTypeCommandCode, commandCodeBase, nil, responsesInbound, "deepseek/deepseek-v4-flash", commandCodeBase + "/v1/chat/completions", &dto.GeneralOpenAIRequest{}},
		{
			"channel rule overrides built-in", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go",
			[]dto.ModelProtocolRule{{Pattern: "glm-*", Protocol: dto.ModelProtocolAnthropic}},
			chatInbound, "glm-5.3", "https://opencode.ai/zen/go/v1/messages", &dto.ClaudeRequest{},
		},
		{
			"channel rules skip built-ins for unmatched models", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go",
			[]dto.ModelProtocolRule{{Pattern: "glm-*", Protocol: dto.ModelProtocolAnthropic}},
			chatInbound, "gpt-5.6-luna", "https://opencode.ai/zen/go/v1/chat/completions", &dto.GeneralOpenAIRequest{},
		},
		{
			"channel rule forwards a listed inbound protocol", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go",
			[]dto.ModelProtocolRule{{Pattern: "glm-5.3", Protocol: dto.ModelProtocolChatCompletions, Protocols: []string{dto.ModelProtocolAnthropic}}},
			messagesInbound, "glm-5.3", "https://opencode.ai/zen/go/v1/messages", &dto.ClaudeRequest{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, info := newTestRelay(t, tt.channelType, tt.baseURL, tt.inbound, tt.model)
			info.ChannelOtherSettings.ModelProtocolRules = tt.rules
			adaptor := NewAdaptor(tt.channelType)
			adaptor.Init(info)

			converted, err := tt.inbound.convert(adaptor, c, info, tt.model)
			require.NoError(t, err)
			assert.IsType(t, tt.wantRequest, converted)
			requestURL, err := adaptor.GetRequestURL(info)
			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, requestURL)
		})
	}
}

func TestAdaptorCommandCodeRoutesByUpstreamModelCatalog(t *testing.T) {
	var catalogRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/provider/v1/models" {
			http.NotFound(w, r)
			return
		}
		catalogRequests.Add(1)
		assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"data":[
			{"id":"deepseek/deepseek-v4-flash","supported_endpoints":["/chat/completions","/responses"]},
			{"id":"gpt-5.5","supported_endpoints":["/chat/completions","/responses"]},
			{"id":"kimi-k3","supported_endpoints":["/messages"]},
			{"id":"no-endpoints"}
		]}`)
	}))
	t.Cleanup(server.Close)
	baseURL := server.URL + "/provider"
	channelID := 670001

	tests := []struct {
		name    string
		rules   []dto.ModelProtocolRule
		inbound inboundRequest
		model   string
		wantURL string
	}{
		{"listed inbound protocol is forwarded", nil, responsesInbound, "deepseek/deepseek-v4-flash", "/v1/responses"},
		{"unlisted inbound uses the catalog order", nil, messagesInbound, "deepseek/deepseek-v4-flash", "/v1/chat/completions"},
		{"built-in preference leads when listed", nil, messagesInbound, "gpt-5.5", "/v1/responses"},
		{"catalog overrides built-in fallback", nil, chatInbound, "kimi-k3", "/v1/messages"},
		{"unknown model falls back to built-ins", nil, messagesInbound, "claude-sonnet-4-6", "/v1/messages"},
		{"channel rule wins over catalog", []dto.ModelProtocolRule{{Pattern: "deepseek/*", Protocol: dto.ModelProtocolAnthropic}}, responsesInbound, "deepseek/deepseek-v4-flash", "/v1/messages"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, info := newTestRelay(t, constant.ChannelTypeCommandCode, baseURL, tt.inbound, tt.model)
			info.ChannelId = channelID
			info.ChannelOtherSettings.ModelProtocolRules = tt.rules
			adaptor := NewAdaptor(constant.ChannelTypeCommandCode)
			adaptor.Init(info)

			_, err := tt.inbound.convert(adaptor, c, info, tt.model)
			require.NoError(t, err)
			requestURL, err := adaptor.GetRequestURL(info)
			require.NoError(t, err)
			assert.Equal(t, baseURL+tt.wantURL, requestURL)
		})
	}
	assert.Equal(t, int32(1), catalogRequests.Load(), "the model list is fetched once and cached per channel")
}

func TestModelCatalogLookup(t *testing.T) {
	ctx := t.Context()
	now := time.Now()
	models := map[string][]string{"m": {dto.ModelProtocolResponses}}

	t.Run("first load waits for the refresh", func(t *testing.T) {
		var catalog modelCatalog
		got := catalog.lookup(ctx, "k", "M", now, func() { catalog.store("k", models, nil, now) })
		assert.Equal(t, []string{dto.ModelProtocolResponses}, got)
	})

	t.Run("failed refresh backs off", func(t *testing.T) {
		var catalog modelCatalog
		var refreshes atomic.Int32
		refresh := func() {
			refreshes.Add(1)
			catalog.store("k", nil, errors.New("unavailable"), now)
		}
		assert.Nil(t, catalog.lookup(ctx, "k", "m", now, refresh))
		assert.Nil(t, catalog.lookup(ctx, "k", "m", now.Add(catalogRetryBackoff-time.Second), refresh))
		assert.Equal(t, int32(1), refreshes.Load())
		catalog.lookup(ctx, "k", "m", now.Add(catalogRetryBackoff), refresh)
		assert.Equal(t, int32(2), refreshes.Load())
	})

	t.Run("stale catalog keeps serving during refresh", func(t *testing.T) {
		var catalog modelCatalog
		catalog.store("k", models, nil, now)
		refreshed := make(chan struct{})
		got := catalog.lookup(ctx, "k", "m", now.Add(catalogTTL), func() { close(refreshed) })
		assert.Equal(t, []string{dto.ModelProtocolResponses}, got)
		<-refreshed
	})
}

func TestParseCatalog(t *testing.T) {
	models, err := parseCatalog([]byte(`[{"id":"A/B","supported_endpoints":["/v1/messages/","/chat/completions","/messages"]},{"id":"c"}]`))
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"a/b": {dto.ModelProtocolAnthropic, dto.ModelProtocolChatCompletions}}, models)

	_, err = parseCatalog([]byte(`{"data":[{"id":"gpt-4o","object":"model"}]}`))
	assert.Error(t, err, "a plain model list without supported_endpoints is not a protocol catalog")
}

func TestAdaptorSetupRequestHeader(t *testing.T) {
	claudeCodeMetadata := []byte(`{"user_id":"{\"device_id\":\"d\",\"session_id\":\"cc-session\"}"}`)
	tests := []struct {
		name          string
		channelType   int
		baseURL       string
		inbound       inboundRequest
		model         string
		clientHeaders map[string]string
		request       dto.Request
		wantSession   string // "uuid" accepts any generated UUID
		wantUserAgent string
		wantAnthropic bool
	}{
		{name: "go generates a session", channelType: constant.ChannelTypeOpenCodeGo, baseURL: "https://opencode.ai/zen/go", inbound: chatInbound, model: "glm-5.3", wantSession: "uuid", wantUserAgent: openCodeUserAgent},
		{name: "go prefers the client session header", channelType: constant.ChannelTypeOpenCodeGo, baseURL: "https://opencode.ai/zen/go", inbound: chatInbound, model: "glm-5.3", clientHeaders: map[string]string{"session_id": "client-session"}, request: &dto.GeneralOpenAIRequest{PromptCacheKey: "cache-key"}, wantSession: "client-session", wantUserAgent: openCodeUserAgent},
		{name: "go uses prompt_cache_key", channelType: constant.ChannelTypeOpenCodeGo, baseURL: "https://opencode.ai/zen/go", inbound: chatInbound, model: "glm-5.3", request: &dto.GeneralOpenAIRequest{PromptCacheKey: "cache-key"}, wantSession: "cache-key", wantUserAgent: openCodeUserAgent},
		{name: "go drops a session with control characters", channelType: constant.ChannelTypeOpenCodeGo, baseURL: "https://opencode.ai/zen/go", inbound: chatInbound, model: "glm-5.3", clientHeaders: map[string]string{"X-OpenCode-Session": "bad\x01id"}, request: &dto.GeneralOpenAIRequest{PromptCacheKey: "cache-key"}, wantSession: "cache-key", wantUserAgent: openCodeUserAgent},
		{name: "zen reads the claude code session", channelType: constant.ChannelTypeOpenCodeZen, baseURL: "https://opencode.ai/zen", inbound: messagesInbound, model: "claude-sonnet-4-6", request: &dto.ClaudeRequest{Metadata: claudeCodeMetadata}, wantSession: "cc-session", wantUserAgent: openCodeUserAgent, wantAnthropic: true},
		{name: "zen without a client session sends none", channelType: constant.ChannelTypeOpenCodeZen, baseURL: "https://opencode.ai/zen", inbound: chatInbound, model: "glm-5.3", wantUserAgent: openCodeUserAgent},
		{name: "zen with a go url generates a session", channelType: constant.ChannelTypeOpenCodeZen, baseURL: "https://opencode.ai/zen/go", inbound: chatInbound, model: "glm-5.3", wantSession: "uuid", wantUserAgent: openCodeUserAgent},
		{name: "zen relay host gets no session", channelType: constant.ChannelTypeOpenCodeZen, baseURL: "https://relay.example/zen", inbound: chatInbound, model: "glm-5.3", clientHeaders: map[string]string{"X-Session-Id": "s"}, wantUserAgent: openCodeUserAgent},
		{name: "command code official host", channelType: constant.ChannelTypeCommandCode, baseURL: "https://api.commandcode.ai/provider", inbound: messagesInbound, model: "claude-sonnet-4-6", wantUserAgent: commandCodeUserAgent, wantAnthropic: true},
		{name: "command code relay host keeps its user agent", channelType: constant.ChannelTypeCommandCode, baseURL: "https://relay.example/provider", inbound: messagesInbound, model: "claude-sonnet-4-6", wantAnthropic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, info := newTestRelay(t, tt.channelType, tt.baseURL, tt.inbound, tt.model)
			for name, value := range tt.clientHeaders {
				c.Request.Header.Set(name, value)
			}
			info.Request = tt.request
			adaptor := NewAdaptor(tt.channelType)
			adaptor.Init(info)
			// Pass-through resolution keeps the request on its own protocol.
			_, err := adaptor.GetRequestURL(info)
			require.NoError(t, err)

			header := http.Header{}
			require.NoError(t, adaptor.SetupRequestHeader(c, &header, info))

			if tt.wantAnthropic {
				assert.Equal(t, "sk-test", header.Get("x-api-key"))
				assert.Equal(t, "2023-06-01", header.Get("anthropic-version"))
				assert.Empty(t, header.Get("Authorization"))
			} else {
				assert.Equal(t, "Bearer sk-test", header.Get("Authorization"))
				assert.Empty(t, header.Get("x-api-key"))
			}
			assert.Equal(t, tt.wantUserAgent, header.Get("User-Agent"))
			session := header.Get(openCodeSessionHeader)
			if tt.wantSession == "uuid" {
				_, err := uuid.Parse(session)
				assert.NoError(t, err, "generated session %q", session)
			} else {
				assert.Equal(t, tt.wantSession, session)
			}
		})
	}
}

func TestAdaptorRejectsOpenCodeProviderOnlyModels(t *testing.T) {
	for _, model := range []string{"gemini-3.8-pro", "opencode/jev-1"} {
		c, _, info := newTestRelay(t, constant.ChannelTypeOpenCodeZen, "https://opencode.ai/zen", chatInbound, model)
		adaptor := NewAdaptor(constant.ChannelTypeOpenCodeZen)
		adaptor.Init(info)
		_, err := chatInbound.convert(adaptor, c, info, model)
		var apiErr *types.NewAPIError
		require.ErrorAs(t, err, &apiErr, model)
		assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode, model)
	}

	commandCodeBase := unavailableCatalog(t)
	c, _, info := newTestRelay(t, constant.ChannelTypeCommandCode, commandCodeBase, chatInbound, "gemini-3.8-pro")
	adaptor := NewAdaptor(constant.ChannelTypeCommandCode)
	adaptor.Init(info)
	_, err := chatInbound.convert(adaptor, c, info, "gemini-3.8-pro")
	assert.NoError(t, err, "only OpenCode reserves gemini models for its Google SDK endpoint")
}

func TestAdaptorFillsDeepSeekReasoningPlaceholders(t *testing.T) {
	history := func(model string) *dto.GeneralOpenAIRequest {
		kept := "kept reasoning"
		return &dto.GeneralOpenAIRequest{Model: model, Messages: []dto.Message{
			{Role: "user", Content: "run it"},
			{Role: "assistant", Content: "", ToolCalls: []byte(`[{"id":"call_1","type":"function","function":{"name":"exec","arguments":"{}"}}]`)},
			{Role: "tool", ToolCallId: "call_1", Content: "ok"},
			{Role: "assistant", Content: "done", ReasoningContent: &kept},
		}}
	}
	commandCodeBase := unavailableCatalog(t)
	tests := []struct {
		name        string
		channelType int
		baseURL     string
		model       string
		wantFilled  bool
	}{
		{"opencode deepseek", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", "opencode-go/DeepSeek-V4-Flash", true},
		{"opencode other model", constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", "glm-5.3", false},
		{"command code deepseek", constant.ChannelTypeCommandCode, commandCodeBase, "deepseek-v4-flash", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, info := newTestRelay(t, tt.channelType, tt.baseURL, chatInbound, tt.model)
			adaptor := NewAdaptor(tt.channelType)
			adaptor.Init(info)
			converted, err := adaptor.ConvertOpenAIRequest(c, info, history(tt.model))
			require.NoError(t, err)
			messages := converted.(*dto.GeneralOpenAIRequest).Messages

			assert.Nil(t, messages[0].ReasoningContent, "user messages are untouched")
			assert.Nil(t, messages[2].ReasoningContent, "tool messages are untouched")
			assert.Equal(t, "kept reasoning", messages[3].GetReasoningContent())
			if tt.wantFilled {
				require.NotNil(t, messages[1].ReasoningContent)
				assert.Equal(t, " ", *messages[1].ReasoningContent)
			} else {
				assert.Nil(t, messages[1].ReasoningContent)
			}
		})
	}
}

func TestAdaptorKeepsRequestsOnTheirOwnProtocolWhenForced(t *testing.T) {
	t.Run("passed-through body", func(t *testing.T) {
		_, _, info := newTestRelay(t, constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", messagesInbound, "gpt-5.6-luna")
		adaptor := NewAdaptor(constant.ChannelTypeOpenCodeGo)
		adaptor.Init(info)
		requestURL, err := adaptor.GetRequestURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://opencode.ai/zen/go/v1/messages", requestURL)
	})

}

func TestAdaptorDoResponseConvertsToClientProtocol(t *testing.T) {
	chatBody := `{"id":"chat_1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
	responsesBody := `{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`
	claudeBody := `{"id":"msg_1","type":"message","model":"m","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`
	// On OpenCode Go, gpt models are served on Responses, minimax models on
	// Messages and glm models on Chat Completions.
	tests := []struct {
		name         string
		inbound      inboundRequest
		model        string
		upstreamBody string
		wantField    string // JSON path in the client reply
		wantValue    string
	}{
		{"chat reply to chat", chatInbound, "glm-5.3", chatBody, "choices.0.message.content", "hello"},
		{"responses reply to responses", responsesInbound, "gpt-5.6-luna", responsesBody, "output.0.content.0.text", "hello"},
		{"responses reply to chat", chatInbound, "gpt-5.6-luna", responsesBody, "choices.0.message.content", "hello"},
		{"responses reply to messages", messagesInbound, "gpt-5.6-luna", responsesBody, "content.0.text", "hello"},
		{"chat reply to responses", responsesInbound, "glm-5.3", chatBody, "output.0.content.0.text", "hello"},
		{"chat reply to messages", messagesInbound, "glm-5.3", chatBody, "content.0.text", "hello"},
		{"messages reply to chat", chatInbound, "minimax-m3", claudeBody, "choices.0.message.content", "hello"},
		{"messages reply to responses", responsesInbound, "minimax-m3", claudeBody, "output.0.content.0.text", "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, recorder, info := newTestRelay(t, constant.ChannelTypeOpenCodeGo, "https://opencode.ai/zen/go", tt.inbound, tt.model)
			adaptor := NewAdaptor(constant.ChannelTypeOpenCodeGo)
			adaptor.Init(info)
			_, err := tt.inbound.convert(adaptor, c, info, tt.model)
			require.NoError(t, err)

			usage, apiErr := adaptor.DoResponse(c, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.upstreamBody))}, info)
			require.Nil(t, apiErr)
			require.IsType(t, &dto.Usage{}, usage)
			assert.Equal(t, 5, usage.(*dto.Usage).TotalTokens)
			assert.Equal(t, tt.wantValue, gjson.Get(recorder.Body.String(), tt.wantField).String(), recorder.Body.String())
		})
	}
}

func TestAdaptorSendsConvertedRequestUpstream(t *testing.T) {
	var gotPath, gotSession, gotUserAgent string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get(openCodeSessionHeader)
		gotUserAgent = r.Header.Get("User-Agent")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(server.Close)

	c, _, info := newTestRelay(t, constant.ChannelTypeOpenCodeGo, server.URL+"/zen/go", chatInbound, "gpt-5.6-luna")
	request := &dto.GeneralOpenAIRequest{Model: "gpt-5.6-luna", PromptCacheKey: "conv-1", Messages: []dto.Message{{Role: "user", Content: "hi"}}}
	info.Request = request
	adaptor := NewAdaptor(constant.ChannelTypeOpenCodeGo)
	adaptor.Init(info)
	converted, err := adaptor.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	body, err := common.Marshal(converted)
	require.NoError(t, err)

	resp, err := adaptor.DoRequest(c, info, strings.NewReader(string(body)))
	require.NoError(t, err)
	require.NoError(t, resp.(*http.Response).Body.Close())

	assert.Equal(t, "/zen/go/v1/responses", gotPath)
	assert.Equal(t, "conv-1", gotSession)
	assert.Equal(t, openCodeUserAgent, gotUserAgent)
	assert.Equal(t, "gpt-5.6-luna", gjson.GetBytes(gotBody, "model").String())
	assert.True(t, gjson.GetBytes(gotBody, "input").Exists(), string(gotBody))
}

func TestCatalogDoesNotFollowCredentialRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"m","supported_endpoints":["/responses"]}]}`)
	}))
	t.Cleanup(target.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)
	_, err := fetchCatalog(origin.URL, http.Header{"Authorization": {"Bearer test-credential"}, "X-Api-Key": {"test-custom"}}, dto.ChannelSettings{})
	require.Error(t, err)
	assert.Zero(t, redirected.Load(), "catalog redirects must not receive any request or credentials")
}

func TestCatalogSeparatesKeysWithinTheSameChannel(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer account-a" {
			close(started)
			<-release
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"glm-5.3","supported_endpoints":["/responses"]}]}`)
	}))
	t.Cleanup(server.Close)
	base := server.URL + "/provider"
	cA, _, infoA := newTestRelay(t, constant.ChannelTypeCommandCode, base, responsesInbound, "glm-5.3")
	cB, _, infoB := newTestRelay(t, constant.ChannelTypeCommandCode, base, responsesInbound, "glm-5.3")
	infoA.ApiKey, infoB.ApiKey = "account-a", "account-b"
	infoB.ChannelId = infoA.ChannelId
	done := make(chan struct{})
	go func() {
		defer close(done)
		adaptor := NewAdaptor(constant.ChannelTypeCommandCode)
		adaptor.Init(infoA)
		_, _ = responsesInbound.convert(adaptor, cA, infoA, "glm-5.3")
	}()
	<-started
	// Let A fail while B starts. B must fetch its own catalog rather than
	// inheriting A's failed refresh and its retry backoff.
	close(release)
	adaptorB := NewAdaptor(constant.ChannelTypeCommandCode)
	adaptorB.Init(infoB)
	_, err := responsesInbound.convert(adaptorB, cB, infoB, "glm-5.3")
	require.NoError(t, err)
	requestURL, err := adaptorB.GetRequestURL(infoB)
	require.NoError(t, err)
	assert.Equal(t, base+"/v1/responses", requestURL)
	<-done
}
