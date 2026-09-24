package vercel

import (
	"errors"

	"github.com/QuantumNous/new-api/relay/channel/newapi"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

// Adaptor targets Vercel AI Gateway. It reuses the multi-protocol passthrough
// from newapi.Adaptor — clients may speak Claude messages, OpenAI chat
// completions, OpenAI responses or Gemini generateContent — and adds
// Gemini-specific request sanitization for Vercel's protocol conversion layer
// (see sanitize.go): hoist system/developer messages on the OAI path and drop
// thinking signatures on the Claude path. Non-Google upstream models pass
// through untouched so anthropic/* thinking signatures stay replayable.
type Adaptor struct {
	newapi.Adaptor
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if geminiUpstream(request.Model, info) {
		request.Messages = hoistOpenAISystemMessages(request.Messages)
	}
	return a.Adaptor.ConvertOpenAIRequest(c, info, request)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if geminiUpstream(request.Model, info) {
		for i := range request.Messages {
			request.Messages[i].Content = sanitizeClaudeThinkingSignatures(request.Messages[i].Content)
		}
	}
	return a.Adaptor.ConvertClaudeRequest(c, info, request)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
