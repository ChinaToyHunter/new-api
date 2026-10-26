package relay

import (
	"maps"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/webtools"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// Gateway web tools on the Chat Completions path (see web_tools.go).
//
// A request asks for hosted web search with web_search_options. The upstream
// calls web search and fetch functions instead, and the client sees none of
// the gateway calls: it receives the model's text as if the model had
// searched by itself.

// chatHostedWebTools returns the gateway functions standing in for the web
// search a chat request asks for, keyed by name, or nil when the gateway does
// not take it over.
func chatHostedWebTools(request *dto.GeneralOpenAIRequest, backend webtools.Backend) map[string]*hostedWebTool {
	// The gateway continues a single choice.
	if request.WebSearchOptions == nil || (request.N != nil && *request.N > 1) {
		return nil
	}
	taken := make(map[string]bool, len(request.Tools))
	for _, tool := range request.Tools {
		taken[tool.Function.Name] = true
	}
	return gatewayWebTools(taken, backend, nil)
}

// gatewayChatRequest returns a copy of the request in which the upstream
// calls the gateway functions instead of searching by itself.
func gatewayChatRequest(request *dto.GeneralOpenAIRequest, tools map[string]*hostedWebTool) (*dto.GeneralOpenAIRequest, error) {
	upstream, err := common.DeepCopy(request)
	if err != nil {
		return nil, err
	}
	upstream.WebSearchOptions = nil
	for _, tool := range orderedWebTools(tools) {
		description, schema := webToolFunction(tool.kind)
		parameters, err := common.Marshal(schema)
		if err != nil {
			return nil, err
		}
		upstream.Tools = append(upstream.Tools, dto.ToolCallRequest{
			Type:     "function",
			Function: dto.FunctionRequest{Name: tool.name, Description: description, Parameters: parameters},
		})
	}
	return upstream, nil
}

// relayChatWithWebTools relays a chat request whose web search the gateway
// runs, and returns the usage of all upstream rounds.
func relayChatWithWebTools(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor, request *dto.GeneralOpenAIRequest, tools map[string]*hostedWebTool, backend webtools.Backend) (*dto.Usage, *types.NewAPIError) {
	upstream, err := gatewayChatRequest(request, tools)
	if err != nil {
		return nil, newConvertRequestFailedError(c, info, err)
	}
	run := newWebToolsRun(backend, operation_setting.GetWebToolsSetting())
	clientStream := info.IsStream
	upstreamModelName, reasoningEffort := info.UpstreamModelName, info.ReasoningEffort
	turn := &chatWebToolsTurn{
		writer:       &webToolsStream{client: c.Writer},
		stream:       clientStream,
		tools:        tools,
		includeUsage: info.ShouldIncludeUsage,
		usage:        make(map[string]any),
	}
	defer func() {
		c.Writer = turn.writer.client
		info.IsStream = clientStream
	}()

	usage := &dto.Usage{}
	var last *chatRound
	clientCalls := false
	maxRounds := run.callLimit + 1
	for round := 0; ; round++ {
		if round > 0 {
			resetWebToolsRound(info, upstreamModelName, reasoningEffort)
		}
		info.IsStream = clientStream
		roundRequest, err := common.DeepCopy(upstream)
		if err != nil {
			return nil, newConvertRequestFailedError(c, info, err)
		}
		result, roundUsage, apiErr := turn.relay(c, info, adaptor, roundRequest)
		if apiErr != nil {
			if turn.started {
				// The client already holds part of the response.
				types.ErrOptionWithSkipRetry()(apiErr)
			}
			return nil, apiErr
		}
		addWebToolsRoundUsage(usage, roundUsage)
		last = result

		var calls []*chatToolCall
		calls, clientCalls = result.gatewayCalls()
		// A call the client runs ends the turn. Gateway calls beside it are
		// dropped unrun; the model can make them again with the client's
		// results in hand.
		if len(calls) == 0 || clientCalls || result.finishReason == "length" {
			break
		}
		for _, call := range calls {
			if call.id == "" {
				call.id = "call_" + common.GetUUID()
			}
		}
		messages := []dto.Message{result.upstreamMessage(calls)}
		for _, call := range calls {
			webResult := run.call(c, tools[call.name], webToolArguments(call.arguments.String()))
			messages = append(messages, dto.Message{Role: "tool", ToolCallId: call.id, Content: webResult.text()})
		}
		if round+1 >= maxRounds || c.Request.Context().Err() != nil {
			break
		}
		upstream.Messages = append(upstream.Messages, messages...)
		// A forced call is made once; later rounds choose freely.
		switch choice := upstream.ToolChoice.(type) {
		case string:
			if choice == "required" {
				upstream.ToolChoice = "auto"
			}
		case map[string]any:
			function, _ := choice["function"].(map[string]any)
			if tools[common.Interface2String(function["name"])] != nil {
				upstream.ToolChoice = "auto"
			}
		}
	}

	finishReason := last.finishReason
	var toolCalls []any
	switch {
	case clientCalls:
		finishReason = "tool_calls"
		for _, index := range last.order {
			if call := last.calls[index]; !call.gateway {
				toolCalls = append(toolCalls, map[string]any{
					"id":       call.id,
					"type":     "function",
					"function": map[string]any{"name": call.name, "arguments": call.arguments.String()},
				})
			}
		}
	case finishReason == "" || finishReason == "tool_calls":
		finishReason = "stop"
	}
	if err := turn.finish(c, finishReason, toolCalls); err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	run.bill(info)
	return usage, nil
}

// chatToolCall is one tool call of a chat round, assembled from its deltas.
type chatToolCall struct {
	id        string
	name      string
	arguments strings.Builder
	gateway   bool
	// shown is the call's index in the client's stream, -1 until it has one.
	shown int
}

// chatRound is one upstream response in the client's Chat Completions format.
type chatRound struct {
	tools map[string]*hostedWebTool
	// head is the first chunk, or the response, whose identity the client
	// keeps for the whole turn.
	head         map[string]any
	text         strings.Builder
	reasoning    strings.Builder
	calls        map[int]*chatToolCall
	order        []int
	finishReason string
	usage        map[string]any
	// shownText reports that a streaming client has received text of the round.
	shownText bool
}

func chatReasoningText(message map[string]any) string {
	if text := common.Interface2String(message["reasoning_content"]); text != "" {
		return text
	}
	return common.Interface2String(message["reasoning"])
}

// apply adds one stream chunk to the round.
func (r *chatRound) apply(chunk map[string]any) {
	if r.head == nil {
		r.head = chunk
	}
	if usage, ok := chunk["usage"].(map[string]any); ok {
		r.usage = usage
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	if reason := common.Interface2String(choice["finish_reason"]); reason != "" {
		r.finishReason = reason
	}
	delta, _ := choice["delta"].(map[string]any)
	r.text.WriteString(common.Interface2String(delta["content"]))
	r.reasoning.WriteString(chatReasoningText(delta))
	toolCalls, _ := delta["tool_calls"].([]any)
	for position, raw := range toolCalls {
		r.applyToolCall(position, raw)
	}
}

// applyResponse takes a whole non-stream response.
func (r *chatRound) applyResponse(response map[string]any) {
	r.head = response
	if usage, ok := response["usage"].(map[string]any); ok {
		r.usage = usage
	}
	choices, _ := response["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	r.finishReason = common.Interface2String(choice["finish_reason"])
	message, _ := choice["message"].(map[string]any)
	r.text.WriteString(common.Interface2String(message["content"]))
	r.reasoning.WriteString(chatReasoningText(message))
	toolCalls, _ := message["tool_calls"].([]any)
	for position, raw := range toolCalls {
		r.applyToolCall(position, raw)
	}
}

// applyToolCall adds a tool call, or a delta of one, to the round. Stream
// deltas carry their index; the calls of a whole message are in order.
func (r *chatRound) applyToolCall(position int, raw any) {
	call, _ := raw.(map[string]any)
	index := position
	if value, ok := call["index"].(float64); ok {
		index = int(value)
	}
	state := r.calls[index]
	if state == nil {
		state = &chatToolCall{shown: -1}
		r.calls[index] = state
		r.order = append(r.order, index)
	}
	if id := common.Interface2String(call["id"]); id != "" {
		state.id = id
	}
	function, _ := call["function"].(map[string]any)
	if name := common.Interface2String(function["name"]); name != "" {
		state.name = name
		state.gateway = r.tools[name] != nil
	}
	state.arguments.WriteString(common.Interface2String(function["arguments"]))
}

// gatewayCalls returns the round's calls of gateway functions, and whether
// the round also calls a tool the client runs.
func (r *chatRound) gatewayCalls() ([]*chatToolCall, bool) {
	var calls []*chatToolCall
	clientCalls := false
	for _, index := range r.order {
		if call := r.calls[index]; call.gateway {
			calls = append(calls, call)
		} else {
			clientCalls = true
		}
	}
	return calls, clientCalls
}

// upstreamMessage is the assistant message with which the next round replays
// this one. Reasoning goes back with it, as thinking models require around
// tool calls.
func (r *chatRound) upstreamMessage(calls []*chatToolCall) dto.Message {
	message := dto.Message{Role: "assistant"}
	if text := r.text.String(); text != "" {
		message.Content = text
	}
	if reasoning := r.reasoning.String(); reasoning != "" {
		message.ReasoningContent = &reasoning
	}
	toolCalls := make([]dto.ToolCallRequest, 0, len(calls))
	for _, call := range calls {
		toolCalls = append(toolCalls, dto.ToolCallRequest{
			ID:       call.id,
			Type:     "function",
			Function: dto.FunctionRequest{Name: call.name, Arguments: call.arguments.String()},
		})
	}
	message.SetToolCalls(toolCalls)
	return message
}

// chatWebToolsTurn presents the upstream rounds of one request to the client
// as a single completion. A streaming client receives the model's text as it
// arrives; the gateway calls never reach it.
type chatWebToolsTurn struct {
	writer       *webToolsStream
	stream       bool
	tools        map[string]*hostedWebTool
	includeUsage bool
	mu           sync.Mutex
	started      bool
	head         map[string]any
	wroteText    bool
	nextCall     int
	text         strings.Builder
	reasoning    strings.Builder
	usage        map[string]any
}

// relay sends one round through the normal Chat Completions path.
func (t *chatWebToolsTurn) relay(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor, request *dto.GeneralOpenAIRequest) (*chatRound, *dto.Usage, *types.NewAPIError) {
	round := &chatRound{tools: t.tools, calls: make(map[int]*chatToolCall)}
	writer := newWebToolsRoundWriter(t.writer, t.stream, func(chunk map[string]any) {
		round.apply(chunk)
		if t.stream {
			t.forward(round, chunk)
		}
	})
	c.Writer = writer
	usage, apiErr := relayTextRequest(c, info, adaptor, request)
	c.Writer = t.writer.client
	if apiErr != nil {
		return nil, nil, apiErr
	}
	response, err := writer.complete()
	if err != nil {
		return nil, nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if response != nil {
		round.applyResponse(response)
	}
	if t.head == nil {
		t.head = round.head
	}
	if round.usage != nil {
		addUsageFields(t.usage, round.usage)
	}
	if !t.stream {
		appendParagraph(&t.text, round.text.String())
		appendParagraph(&t.reasoning, round.reasoning.String())
	}
	return round, usage, nil
}

// appendParagraph adds the text of a later round as a new paragraph.
func appendParagraph(text *strings.Builder, paragraph string) {
	if paragraph == "" {
		return
	}
	if text.Len() > 0 {
		text.WriteString("\n\n")
	}
	text.WriteString(paragraph)
}

// forward writes the client's part of one live upstream chunk: text,
// reasoning and the calls of client tools.
func (t *chatWebToolsTurn) forward(round *chatRound, chunk map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.head == nil {
		t.head = chunk
	}
	// The turn sends usage, the finish reason and [DONE] once, at its end.
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	shown := make(map[string]any, len(delta))
	for key, value := range delta {
		switch key {
		case "role":
			if !t.started {
				shown[key] = value
			}
		case "content":
			text := common.Interface2String(value)
			if text == "" {
				continue
			}
			// Text of a later round starts a new paragraph.
			if t.wroteText && !round.shownText {
				text = "\n\n" + text
			}
			round.shownText, t.wroteText = true, true
			shown[key] = text
		case "tool_calls":
			calls, _ := value.([]any)
			var clientCalls []any
			for position, raw := range calls {
				call, _ := raw.(map[string]any)
				index := position
				if number, ok := call["index"].(float64); ok {
					index = int(number)
				}
				state := round.calls[index]
				if state == nil || state.gateway {
					continue
				}
				if state.shown < 0 {
					state.shown = t.nextCall
					t.nextCall++
				}
				call = maps.Clone(call)
				call["index"] = state.shown
				clientCalls = append(clientCalls, call)
			}
			if len(clientCalls) > 0 {
				shown[key] = clientCalls
			}
		default:
			shown[key] = value
		}
	}
	if len(shown) == 0 {
		return
	}
	t.started = true
	t.writer.writeData(t.chunk(map[string]any{"index": 0, "delta": shown, "finish_reason": nil}))
}

// chunk wraps choices in the identity of the first round.
func (t *chatWebToolsTurn) chunk(choices ...any) map[string]any {
	chunk := make(map[string]any, 6)
	for _, key := range []string{"id", "created", "model", "system_fingerprint"} {
		if value, ok := t.head[key]; ok {
			chunk[key] = value
		}
	}
	chunk["object"] = "chat.completion.chunk"
	chunk["choices"] = append([]any{}, choices...)
	return chunk
}

// finish ends the completion with the finish reason of the turn and the
// usage of all rounds.
func (t *chatWebToolsTurn) finish(c *gin.Context, finishReason string, toolCalls []any) error {
	if t.stream {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.writer.writeData(t.chunk(map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finishReason}))
		if t.includeUsage && len(t.usage) > 0 {
			usageChunk := t.chunk()
			usageChunk["usage"] = t.usage
			t.writer.writeData(usageChunk)
		}
		t.writer.write("data: [DONE]\n\n")
		return nil
	}
	message := map[string]any{"role": "assistant", "content": t.text.String()}
	if t.reasoning.Len() > 0 {
		message["reasoning_content"] = t.reasoning.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	response := maps.Clone(t.head)
	if response == nil {
		response = make(map[string]any)
	}
	response["object"] = "chat.completion"
	response["choices"] = []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}}
	delete(response, "usage")
	if len(t.usage) > 0 {
		response["usage"] = t.usage
	}
	data, err := common.Marshal(response)
	if err != nil {
		return err
	}
	c.Data(http.StatusOK, "application/json", data)
	return nil
}
