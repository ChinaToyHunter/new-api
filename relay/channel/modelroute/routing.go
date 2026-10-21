package modelroute

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	openCodeHost    = "opencode.ai"
	commandCodeHost = "api.commandcode.ai"

	// opencode.ai and api.commandcode.ai sit behind Cloudflare bot rules that
	// reject programming-library User-Agents with error 1010. Requests carry the
	// User-Agent of a client those gateways serve; a channel header override
	// still takes precedence.
	openCodeUserAgent    = "opencode/1.0.0"
	commandCodeUserAgent = "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color"

	openCodeSessionHeader     = "X-OpenCode-Session"
	maxOpenCodeSessionIDBytes = 255
)

// openCodeSessionHeaders are the client headers that carry a stable
// conversation ID, in priority order. Per-turn request IDs are excluded: a new
// value every turn would miss the upstream prompt cache.
var openCodeSessionHeaders = []string{
	openCodeSessionHeader,
	"session-id",
	"session_id",
	"conversation_id",
	"X-Session-Affinity",
	"X-Session-Id",
	"X-Conversation-ID",
	"X-Claude-Code-Session-Id",
}

// openCodeGoModels is the OpenCode Go model catalog published in its docs.
var openCodeGoModels = []string{
	"grok-4.7",
	"grok-4.6",
	"gpt-5.6-luna",
	"glm-5.3-flash",
	"glm-5.3",
	"glm-5.2",
	"glm-5.1",
	"kimi-k3",
	"kimi-k2.7-code",
	"kimi-k2.6",
	"longcat-2.0",
	"deepseek-v4-pro",
	"deepseek-v4-flash",
	"deepseek-v4-flash-vision-exp",
	"mimo-v2.5",
	"mimo-v2.5-pro",
	"minimax-m3",
	"minimax-m2.7",
	"minimax-m2.5",
	"muse-spark-1.3-contributor",
	"muse-spark-1.2-contributor",
	"qwen3.8-max",
	"qwen3.8-flash",
	"qwen3.7-max",
	"qwen3.7-plus",
	"qwen3.6-plus",
	"hy4-preview",
	"hy3",
	"omen-alpha",
}

// builtinRules returns the default model → upstream protocol table of a
// channel type. Models no rule matches use Chat Completions.
func builtinRules(channelType int) []dto.ModelProtocolRule {
	switch channelType {
	case constant.ChannelTypeOpenCodeZen:
		// https://opencode.ai/docs/zen/ endpoint table.
		return []dto.ModelProtocolRule{
			{Pattern: "grok-*", Protocol: dto.ModelProtocolResponses},
			{Pattern: "gpt-*", Protocol: dto.ModelProtocolResponses},
			{Pattern: "muse-spark-*", Protocol: dto.ModelProtocolResponses},
			{Pattern: "claude-*", Protocol: dto.ModelProtocolAnthropic},
			{Pattern: "qwen3.8-max", Protocol: dto.ModelProtocolChatCompletions},
			{Pattern: "qwen*", Protocol: dto.ModelProtocolAnthropic},
		}
	case constant.ChannelTypeOpenCodeGo:
		return []dto.ModelProtocolRule{
			{Pattern: "grok-*", Protocol: dto.ModelProtocolResponses},
			{Pattern: "gpt-*", Protocol: dto.ModelProtocolResponses},
			{Pattern: "muse-spark-*", Protocol: dto.ModelProtocolResponses},
			{Pattern: "minimax-*", Protocol: dto.ModelProtocolAnthropic},
			{Pattern: "qwen*", Protocol: dto.ModelProtocolAnthropic},
		}
	case constant.ChannelTypeCommandCode:
		// Claude models are only served on Messages; GPT models also accept
		// Chat Completions. Other models are only guaranteed on Chat
		// Completions; the upstream model catalog tells which also serve
		// Responses.
		return []dto.ModelProtocolRule{
			{Pattern: "claude-*", Protocol: dto.ModelProtocolAnthropic},
			{Pattern: "gpt-*", Protocol: dto.ModelProtocolResponses, Protocols: []string{dto.ModelProtocolResponses, dto.ModelProtocolChatCompletions}},
		}
	default:
		return nil
	}
}

func isOpenCode(channelType int) bool {
	return channelType == constant.ChannelTypeOpenCodeZen || channelType == constant.ChannelTypeOpenCodeGo
}

// routingModel is the model ID that routing rules match: lower case, without
// the provider prefixes OpenCode clients put in front of model IDs.
func routingModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"opencode-go/", "opencode_go/", "opencode/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	return model
}

// rejectUnsupportedModel rejects models that OpenCode only serves on its
// provider-specific endpoints: Gemini on the Google SDK endpoint and jev on
// the System One endpoint.
func rejectUnsupportedModel(channelType int, model string) error {
	if !isOpenCode(channelType) {
		return nil
	}
	normalized := routingModel(model)
	if !strings.HasPrefix(normalized, "gemini-") && !strings.HasPrefix(normalized, "jev-") {
		return nil
	}
	return types.NewErrorWithStatusCode(
		fmt.Errorf("model %s is not supported on the OpenCode standard gateway: gemini models require the Google SDK endpoint and jev models require the System One endpoint", model),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
}

// EndpointURL joins an API path such as /v1/models to a channel base URL,
// without doubling the /v1 segment when the base URL already ends with it.
func EndpointURL(baseURL string, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/v1") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

// IsCommandCodeOfficialURL reports whether a Command Code channel targets the
// official endpoint; an empty base URL uses the official default.
func IsCommandCodeOfficialURL(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return true
	}
	parsed, err := url.Parse(baseURL)
	return err == nil && strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), commandCodeHost)
}

// UpstreamUserAgent returns the User-Agent that requests from a channel to its
// gateway must carry, or "" when the channel keeps the default one. OpenCode
// channels always send it; Command Code channels only to the official host,
// which is where the Cloudflare rule applies.
func UpstreamUserAgent(channelType int, baseURL string) string {
	switch {
	case isOpenCode(channelType):
		return openCodeUserAgent
	case channelType == constant.ChannelTypeCommandCode && IsCommandCodeOfficialURL(baseURL):
		return commandCodeUserAgent
	default:
		return ""
	}
}

// openCodeSessionID returns the X-OpenCode-Session value of a request.
// OpenCode Go rejects requests without one, and a stable value lets the
// upstream prompt cache hit. The value comes from the client's session
// headers, then the documented body fields (prompt_cache_key, Anthropic
// metadata.user_id). Requests to OpenCode Go generate a random value as a last
// resort; Zen channels send the header only to the official host and only when
// the client provides a session.
func openCodeSessionID(c *gin.Context, info *relaycommon.RelayInfo, channelType int) string {
	parsed, err := url.Parse(strings.TrimSpace(info.ChannelBaseUrl))
	official := err == nil && strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), openCodeHost)
	goPlan := channelType == constant.ChannelTypeOpenCodeGo || official && strings.Contains(parsed.Path, "/zen/go")
	if !goPlan && !official {
		return ""
	}
	if c != nil && c.Request != nil {
		for _, name := range openCodeSessionHeaders {
			if sessionID := sanitizeSessionID(c.GetHeader(name)); sessionID != "" {
				return sessionID
			}
		}
	}
	if sessionID := sanitizeSessionID(requestSessionID(info.Request)); sessionID != "" {
		return sessionID
	}
	if goPlan {
		return uuid.NewString()
	}
	return ""
}

// requestSessionID reads the conversation ID clients put in the request body:
// prompt_cache_key on Chat Completions and Responses, metadata.user_id on
// Messages. Claude Code encodes user_id as JSON with a session_id member.
func requestSessionID(request dto.Request) string {
	var promptCacheKey string
	var metadata []byte
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		promptCacheKey, metadata = req.PromptCacheKey, req.Metadata
	case *dto.OpenAIResponsesRequest:
		promptCacheKey, metadata = gjson.ParseBytes(req.PromptCacheKey).String(), req.Metadata
	case *dto.ClaudeRequest:
		metadata = req.Metadata
	}
	if key := strings.TrimSpace(promptCacheKey); key != "" {
		return key
	}
	userID := strings.TrimSpace(gjson.GetBytes(metadata, "user_id").String())
	if strings.HasPrefix(userID, "{") {
		if sessionID := strings.TrimSpace(gjson.Get(userID, "session_id").String()); sessionID != "" {
			return sessionID
		}
	}
	return userID
}

// sanitizeSessionID drops values that cannot be a session ID: invalid UTF-8,
// control characters, or more than maxOpenCodeSessionIDBytes bytes.
func sanitizeSessionID(raw string) string {
	sessionID := strings.TrimSpace(raw)
	if sessionID == "" || len(sessionID) > maxOpenCodeSessionIDBytes || !utf8.ValidString(sessionID) {
		return ""
	}
	if strings.ContainsFunc(sessionID, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return sessionID
}

// fillDeepSeekReasoningPlaceholders adds a placeholder reasoning_content to
// assistant messages that lack one when OpenCode forwards a DeepSeek model.
// DeepSeek thinking mode rejects a history whose assistant messages do not pass
// reasoning_content back, and OpenCode relays that 400 unchanged.
func fillDeepSeekReasoningPlaceholders(channelType int, request *dto.GeneralOpenAIRequest) {
	if !isOpenCode(channelType) || !strings.HasPrefix(routingModel(request.Model), "deepseek") {
		return
	}
	for i := range request.Messages {
		message := &request.Messages[i]
		if message.Role != "assistant" || message.ReasoningContent != nil && *message.ReasoningContent != "" {
			continue
		}
		placeholder := " "
		message.ReasoningContent = &placeholder
	}
}
