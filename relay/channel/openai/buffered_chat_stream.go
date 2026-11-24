package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// writeBufferedChatStream treats the complete upstream JSON as one set of
// deltas. It does not invent token arrival times or repair truncated answers.
func writeBufferedChatStream(c *gin.Context, info *relaycommon.RelayInfo, response dto.OpenAITextResponse, body []byte) (*dto.Usage, *types.NewAPIError) {
	type choiceDelta struct {
		Index        int                        `json:"index"`
		Delta        map[string]json.RawMessage `json:"delta"`
		Logprobs     json.RawMessage            `json:"logprobs"`
		FinishReason *string                    `json:"finish_reason"`
	}
	id := response.Id
	if id == "" {
		id = helper.GetResponseID(c)
	}
	model := response.Model
	if model == "" {
		model = info.UpstreamModelName
	}
	created := ojson.GetBytes(body, "created").Int()
	if created == 0 {
		created = time.Now().Unix()
	}
	type chunk struct {
		ID                string          `json:"id"`
		Object            string          `json:"object"`
		Created           int64           `json:"created"`
		Model             string          `json:"model"`
		SystemFingerprint json.RawMessage `json:"system_fingerprint,omitempty"`
		Choices           []choiceDelta   `json:"choices"`
		Usage             *dto.Usage      `json:"usage"`
	}
	base := chunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
		SystemFingerprint: json.RawMessage(ojson.GetBytes(body, "system_fingerprint").Raw)}
	roleFrame, messageFrame, reasoningFrame, finishFrame := base, base, base, base
	rawChoices := ojson.GetBytesNoCopy(body, "choices").Array()
	if len(response.Choices) == 0 || len(rawChoices) != len(response.Choices) {
		return nil, types.NewOpenAIError(fmt.Errorf("upstream chat response has no valid choices"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	missingFinishReason := false
	for i, choice := range response.Choices {
		var delta map[string]json.RawMessage
		if err := common.UnmarshalJsonStr(rawChoices[i].Get("message").Raw, &delta); err != nil || delta == nil {
			return nil, types.NewOpenAIError(fmt.Errorf("upstream chat choice has no valid message"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		reason := choice.FinishReason
		if strings.TrimSpace(reason) == "" {
			// Keep usable text without guessing why generation stopped. Do not
			// deliver a potentially incomplete tool call to an agent client.
			toolCalls := ojson.ParseBytes(delta["tool_calls"])
			hasTools := toolCalls.Exists() && toolCalls.Type != ojson.Null && (!toolCalls.IsArray() || len(toolCalls.Array()) > 0)
			legacyCall := delta["function_call"]
			// The Chat delta formatter expects a string, not content parts.
			text, _ := choice.Message.Content.(string)
			if strings.TrimSpace(text) == "" || hasTools || (len(legacyCall) > 0 && string(legacyCall) != "null") {
				return nil, types.NewOpenAIError(fmt.Errorf("upstream chat choice has no finish reason"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
			}
			missingFinishReason = true
		} else {
			finishFrame.Choices = append(finishFrame.Choices, choiceDelta{Index: choice.Index, Delta: map[string]json.RawMessage{}, FinishReason: &reason})
		}
		role := delta["role"]
		if len(role) == 0 || string(role) == "null" {
			role = json.RawMessage(`"assistant"`)
		}
		delete(delta, "role")
		roleFrame.Choices = append(roleFrame.Choices, choiceDelta{Index: choice.Index, Delta: map[string]json.RawMessage{"role": role}})
		if raw, ok := delta["tool_calls"]; ok && string(raw) != "null" {
			// Preserve vendor fields such as Gemini's thought signature. The
			// position in the final message becomes the tool index in the delta.
			var toolCalls []map[string]json.RawMessage
			err := common.Unmarshal(raw, &toolCalls)
			for index, tool := range toolCalls {
				if tool == nil {
					err = fmt.Errorf("upstream chat tool call is null")
					break
				}
				tool["index"] = strconv.AppendInt(nil, int64(index), 10)
			}
			if err == nil {
				delta["tool_calls"], err = common.Marshal(toolCalls)
			}
			if err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
			}
		}
		if info.ChannelSetting.ThinkingToContent && choice.Message.GetReasoningContent() != "" {
			// The existing formatter expects reasoning and text on separate frames.
			reasoningFrame.Choices = append(reasoningFrame.Choices, choiceDelta{Index: choice.Index, Delta: map[string]json.RawMessage{
				"reasoning": delta["reasoning"], "reasoning_content": delta["reasoning_content"],
			}})
			delete(delta, "reasoning")
			delete(delta, "reasoning_content")
		}
		logprobs := rawChoices[i].Get("logprobs").Raw
		if logprobs == "" {
			logprobs = "null"
		}
		messageFrame.Choices = append(messageFrame.Choices, choiceDelta{Index: choice.Index, Delta: delta, Logprobs: json.RawMessage(logprobs)})
	}

	frames := make([]chunk, 0, 5)
	frames = append(frames, roleFrame)
	if len(reasoningFrame.Choices) > 0 {
		frames = append(frames, reasoningFrame)
	}
	frames = append(frames, messageFrame)
	if len(finishFrame.Choices) > 0 {
		frames = append(frames, finishFrame)
	}
	if info.ShouldIncludeUsage {
		base.Choices = []choiceDelta{}
		base.Usage = &response.Usage
		frames = append(frames, base)
	}
	encoded := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		data, err := common.Marshal(frame)
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		encoded = append(encoded, data)
	}

	helper.SetEventStreamHeaders(c)
	info.SetFirstResponseTime()
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.RequireTerminal()
	for _, data := range encoded {
		// Use the raw frame unless the operator explicitly requested OpenAI
		// format normalization or reasoning-to-content conversion.
		if err := HandleStreamFormat(c, info, string(data), info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
			return &response.Usage, nil
		}
	}
	if err := helper.StringData(c, "[DONE]"); err != nil {
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
		return &response.Usage, nil
	}
	if missingFinishReason {
		info.StreamStatus.MarkIncomplete("missing_finish_reason")
	} else {
		info.StreamStatus.MarkCompleted()
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	return &response.Usage, nil
}
