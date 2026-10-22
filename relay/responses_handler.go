package relay

import (
	"fmt"
	"net/http"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/webtools"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func ResponsesHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	var responsesReq *dto.OpenAIResponsesRequest
	switch req := info.Request.(type) {
	case *dto.OpenAIResponsesRequest:
		responsesReq = req
	case *dto.OpenAIResponsesCompactionRequest:
		// Only fields documented for POST /v1/responses/compact are forwarded:
		// model, input, instructions, previous_response_id, prompt_cache_key,
		// prompt_cache_options, prompt_cache_retention, service_tier.
		// Undocumented Codex-parity fields (tools, reasoning, text) are parsed
		// for client compatibility but intentionally not sent upstream.
		responsesReq = &dto.OpenAIResponsesRequest{
			Model:                req.Model,
			Input:                req.Input,
			Instructions:         req.Instructions,
			PreviousResponseID:   req.PreviousResponseID,
			ParallelToolCalls:    req.ParallelToolCalls,
			ServiceTier:          req.ServiceTier,
			PromptCacheKey:       req.PromptCacheKey,
			PromptCacheOptions:   req.PromptCacheOptions,
			PromptCacheRetention: req.PromptCacheRetention,
		}
	default:
		return types.NewErrorWithStatusCode(
			fmt.Errorf("invalid request type, expected dto.OpenAIResponsesRequest or dto.OpenAIResponsesCompactionRequest, got %T", info.Request),
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	if info.RelayMode == relayconstant.RelayModeResponses {
		info.InitChannelMeta(c)
		if webtools.GatewayMode(c, info) {
			backend := webtools.NewBackend(c, info)
			if tools := responsesHostedWebTools(responsesReq, backend); tools != nil {
				usage, apiErr := relayResponsesWithWebTools(c, info, responsesReq, tools, backend)
				if apiErr != nil {
					return apiErr
				}
				ConsumeResponsesQuota(c, info, usage)
				return nil
			}
		}
	}

	usageDto, newAPIError := relayResponses(c, info, responsesReq)
	if newAPIError != nil {
		return newAPIError
	}
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		originModelName := info.OriginModelName
		originPriceData := info.PriceData

		_, err := helper.ModelPriceHelper(c, info, info.GetEstimatePromptTokens(), &types.TokenCountMeta{})
		if err != nil {
			info.OriginModelName = originModelName
			info.PriceData = originPriceData
			return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithSkipRetry(), types.ErrOptionWithStatusCode(http.StatusBadRequest))
		}
		service.PostTextConsumeQuota(c, info, usageDto, nil)

		info.OriginModelName = originModelName
		info.PriceData = originPriceData
		return nil
	}

	ConsumeResponsesQuota(c, info, usageDto)
	return nil
}

// relayResponses prepares a Responses request for the selected channel,
// sends it, and relays the response to c.Writer.
func relayResponses(c *gin.Context, info *relaycommon.RelayInfo, request *dto.OpenAIResponsesRequest) (*dto.Usage, *types.NewAPIError) {
	adaptor, requestBody, closer, apiErr := PrepareResponsesRequest(c, info, request)
	if apiErr != nil {
		return nil, apiErr
	}
	defer closer.Close()

	var httpResp *http.Response
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	if resp != nil {
		httpResp = resp.(*http.Response)

		if httpResp.StatusCode != http.StatusOK {
			newAPIError := service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			// reset status code 重置状态码
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return nil, newAPIError
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return nil, newAPIError
	}
	return usage.(*dto.Usage), nil
}

// ConsumeResponsesQuota applies the same settlement dispatch to HTTP and
// WebSocket Responses usage. Compact requests keep their separate repricing.
func ConsumeResponsesQuota(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) {
	if strings.HasPrefix(info.OriginModelName, "gpt-4o-audio") {
		service.PostAudioConsumeQuota(c, info, usage, nil)
		return
	}
	service.PostTextConsumeQuota(c, info, usage, nil)
}
