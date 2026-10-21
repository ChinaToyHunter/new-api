package modelroute

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

var errUnsupportedEndpoint = errors.New("model-routed channels only support chat completions, responses and messages requests")

var upstreamPaths = map[string]string{
	dto.ModelProtocolChatCompletions: "/v1/chat/completions",
	dto.ModelProtocolResponses:       "/v1/responses",
	dto.ModelProtocolAnthropic:       "/v1/messages",
}

// Adaptor relays to a model-routed gateway (OpenCode Zen/Go, Command Code),
// where one API key serves each model on Chat Completions, Responses or
// Anthropic Messages. Each attempt looks up the protocols that serve the
// requested model, forwards the request unchanged when its own protocol is
// one of them and otherwise converts it to the preferred protocol.
type Adaptor struct {
	channelType int
	openai      openai.Adaptor
	claude      claude.Adaptor

	resolved bool
	// inbound is the protocol of the client request; upstream is the protocol
	// sent to the gateway.
	inbound  string
	upstream string
}

// NewAdaptor returns an adaptor for channelType; Init switches it to the type
// of the relayed channel.
func NewAdaptor(channelType int) *Adaptor {
	return &Adaptor{channelType: channelType}
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	if info.ChannelType != 0 {
		a.channelType = info.ChannelType
	}
	if strings.TrimSpace(info.ChannelBaseUrl) == "" {
		info.ChannelBaseUrl = constant.GetChannelBaseURL(a.channelType)
	}
	a.openai.Init(info)
	a.claude.Init(info)
}

// resolve fixes the protocols of the attempt on first use. forceInbound keeps
// a passed-through body on its original protocol, since it cannot be converted.
func (a *Adaptor) resolve(c *gin.Context, info *relaycommon.RelayInfo, inbound string, forceInbound bool) error {
	if a.resolved {
		return nil
	}
	if err := rejectUnsupportedModel(a.channelType, info.UpstreamModelName); err != nil {
		return err
	}
	a.inbound = inbound
	a.upstream = inbound
	if !forceInbound {
		if set := a.protocolSet(c, info); !slices.Contains(set, inbound) {
			a.upstream = set[0]
		}
	}
	a.resolved = true
	return nil
}

// protocolSet returns the protocols that serve the model, preferred first: the
// first matching channel rule; else the upstream model catalog, led by the
// built-in preference when the catalog lists it; else the built-in rules,
// unless the channel configures rules of its own; else Chat Completions.
func (a *Adaptor) protocolSet(c *gin.Context, info *relaycommon.RelayInfo) []string {
	model := routingModel(info.UpstreamModelName)
	channelRules := info.ChannelOtherSettings.ModelProtocolRules
	if set, ok := dto.MatchModelProtocolRules(channelRules, model); ok {
		return set
	}
	builtin := builtinRules(a.channelType)
	if catalog := a.catalogProtocols(c, info); len(catalog) > 0 {
		preferred := dto.ModelProtocolChatCompletions
		if set, ok := dto.MatchModelProtocolRules(builtin, model); ok {
			preferred = set[0]
		}
		if slices.Contains(catalog, preferred) {
			return dto.ModelProtocolRule{Protocol: preferred, Protocols: catalog}.ProtocolSet()
		}
		return catalog
	}
	if len(channelRules) == 0 {
		if set, ok := dto.MatchModelProtocolRules(builtin, model); ok {
			return set
		}
	}
	return []string{dto.ModelProtocolChatCompletions}
}

// catalogProtocols returns the protocols the Command Code model list names for
// the model, or nil when the list is unavailable.
func (a *Adaptor) catalogProtocols(c *gin.Context, info *relaycommon.RelayInfo) []string {
	if a.channelType != constant.ChannelTypeCommandCode || strings.TrimSpace(info.UpstreamModelName) == "" {
		return nil
	}
	modelsURL := EndpointURL(info.ChannelBaseUrl, "/v1/models")
	header := http.Header{}
	header.Set("Accept", "application/json")
	header.Set("Authorization", "Bearer "+info.ApiKey)
	if userAgent := UpstreamUserAgent(a.channelType, info.ChannelBaseUrl); userAgent != "" {
		header.Set("User-Agent", userAgent)
	}
	overrides, err := channel.ResolveHeaderOverride(info, c)
	if err != nil {
		return nil
	}
	for name, value := range overrides {
		header.Set(name, value)
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	// Selected keys and tenant header overrides can expose different catalogs.
	// Header.Write sorts field names; only the digest is retained in the key.
	identity := sha256.New()
	_, _ = fmt.Fprintf(identity, "%s\x00", info.ApiKey)
	_ = header.Write(identity)
	key := fmt.Sprintf("%d#%s#%x", info.ChannelId, modelsURL, identity.Sum(nil))
	channelID, setting := info.ChannelId, info.ChannelSetting
	return catalogs.lookup(ctx, key, info.UpstreamModelName, time.Now(), func() {
		models, err := fetchCatalog(modelsURL, header, setting)
		if err != nil {
			common.SysLog(common.LogText("failed to refresh model protocol catalog: channel_id=%d", channelID))
		}
		catalogs.store(key, models, err, time.Now())
	})
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if info.RelayMode != relayconstant.RelayModeChatCompletions {
		return nil, errUnsupportedEndpoint
	}
	if err := a.resolve(c, info, dto.ModelProtocolChatCompletions, false); err != nil {
		return nil, err
	}
	switch a.upstream {
	case dto.ModelProtocolAnthropic:
		return a.claude.ConvertOpenAIRequest(c, info, request)
	case dto.ModelProtocolResponses:
		return a.convertToResponses(c, info, request)
	default:
		return a.convertOpenAIChat(c, info, request)
	}
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if err := a.resolve(c, info, dto.ModelProtocolAnthropic, false); err != nil {
		return nil, err
	}
	switch a.upstream {
	case dto.ModelProtocolAnthropic:
		return a.claude.ConvertClaudeRequest(c, info, request)
	case dto.ModelProtocolResponses:
		return a.convertToResponses(c, info, request)
	default:
		return a.convertToChat(c, info, request)
	}
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if err := a.resolve(c, info, dto.ModelProtocolResponses, false); err != nil {
		return nil, err
	}
	switch a.upstream {
	case dto.ModelProtocolAnthropic:
		return a.claude.ConvertOpenAIResponsesRequest(c, info, request)
	case dto.ModelProtocolChatCompletions:
		return a.convertToChat(c, info, &request)
	default:
		return a.openai.ConvertOpenAIResponsesRequest(c, info, request)
	}
}

// convertToChat converts a Messages or Responses request for a Chat
// Completions upstream. Streams ask for usage, because the client protocol
// reports usage in its own format and never carries stream_options.
func (a *Adaptor) convertToChat(c *gin.Context, info *relaycommon.RelayInfo, request any) (any, error) {
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)
	if err != nil {
		return nil, err
	}
	chatRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	if info.SupportStreamOptions && info.IsStream {
		chatRequest.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	}
	return a.convertOpenAIChat(c, info, chatRequest)
}

// convertOpenAIChat applies the OpenAI chat request rules. They run as the
// OpenAI channel type so that stream_options, which these gateways accept,
// reaches the upstream.
func (a *Adaptor) convertOpenAIChat(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	channelType := info.ChannelType
	info.ChannelType = constant.ChannelTypeOpenAI
	converted, err := a.openai.ConvertOpenAIRequest(c, info, request)
	info.ChannelType = channelType
	if err != nil {
		return nil, err
	}
	if chatRequest, ok := converted.(*dto.GeneralOpenAIRequest); ok {
		fillDeepSeekReasoningPlaceholders(a.channelType, chatRequest)
	}
	return converted, nil
}

// convertToResponses converts a Chat Completions or Messages request for a
// Responses upstream.
func (a *Adaptor) convertToResponses(c *gin.Context, info *relaycommon.RelayInfo, request any) (any, error) {
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAIResponses, request)
	if err != nil {
		return nil, err
	}
	responsesRequest, ok := result.Value.(*dto.OpenAIResponsesRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI Responses request, got %T", result.Value)
	}
	return a.openai.ConvertOpenAIResponsesRequest(c, info, *responsesRequest)
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errUnsupportedEndpoint
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errUnsupportedEndpoint
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errUnsupportedEndpoint
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errUnsupportedEndpoint
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errUnsupportedEndpoint
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if !a.resolved {
		// No conversion ran: the client body is passed through as received.
		inbound := ""
		switch {
		case info.RelayFormat == types.RelayFormatClaude:
			inbound = dto.ModelProtocolAnthropic
		case info.RelayMode == relayconstant.RelayModeResponses:
			inbound = dto.ModelProtocolResponses
		case info.RelayMode == relayconstant.RelayModeChatCompletions:
			inbound = dto.ModelProtocolChatCompletions
		default:
			return "", errUnsupportedEndpoint
		}
		if err := a.resolve(nil, info, inbound, true); err != nil {
			return "", err
		}
	}
	return EndpointURL(info.ChannelBaseUrl, upstreamPaths[a.upstream]), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	if a.upstream == dto.ModelProtocolAnthropic {
		header.Set("x-api-key", info.ApiKey)
		anthropicVersion := c.Request.Header.Get("anthropic-version")
		if anthropicVersion == "" {
			anthropicVersion = "2023-06-01"
		}
		header.Set("anthropic-version", anthropicVersion)
		claude.CommonClaudeHeadersOperation(c, header, info)
	} else {
		header.Set("Authorization", "Bearer "+info.ApiKey)
	}
	if userAgent := UpstreamUserAgent(a.channelType, info.ChannelBaseUrl); userAgent != "" {
		header.Set("User-Agent", userAgent)
	}
	if isOpenCode(a.channelType) {
		if sessionID := openCodeSessionID(c, info, a.channelType); sessionID != "" {
			header.Set(openCodeSessionHeader, sessionID)
		}
	}
	return nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch {
	case a.upstream == dto.ModelProtocolAnthropic:
		// The Claude handlers reply in info.RelayFormat.
		return a.claude.DoResponse(c, resp, info)
	case a.upstream == dto.ModelProtocolResponses && a.inbound != dto.ModelProtocolResponses:
		// These handlers convert the Responses reply into info.RelayFormat.
		if info.IsStream {
			return openai.OaiResponsesToChatStreamHandler(c, info, resp)
		}
		return openai.OaiResponsesToChatHandler(c, info, resp)
	case a.upstream == dto.ModelProtocolChatCompletions && a.inbound == dto.ModelProtocolResponses:
		if info.IsStream {
			return openai.OaiChatToResponsesStreamHandler(c, info, resp)
		}
		return openai.OaiChatToResponsesHandler(c, info, resp)
	default:
		// Same-protocol replies, and Chat Completions replies to Messages
		// requests, which the OpenAI handlers convert into info.RelayFormat.
		return a.openai.DoResponse(c, resp, info)
	}
}

func (a *Adaptor) GetModelList() []string {
	if a.channelType == constant.ChannelTypeOpenCodeGo {
		return openCodeGoModels
	}
	return nil
}

func (a *Adaptor) GetChannelName() string {
	if a.channelType == constant.ChannelTypeCommandCode {
		return "commandcode"
	}
	return "opencode"
}
