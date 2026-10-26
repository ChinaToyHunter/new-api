package relay

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/webtools"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// Gateway web tools on the Anthropic Messages path (see web_tools.go).
//
// The client receives each gateway call as a server_tool_use block and its
// result block. A later request that replays those blocks has them turned
// back into function calls and results, so the upstream sees the same history
// again.

const (
	claudeServerToolIDPrefix = "srvtoolu_"
	// claudeSearchEnvelopePrefix marks the encrypted_content of a gateway
	// search result, which carries the result data the gateway replays.
	claudeSearchEnvelopePrefix = "newapi.ws1."
)

// claudeHostedWebTools returns the hosted web tools of a request the gateway
// runs, keyed by name. Nothing is taken over while no channel can run either
// tool.
func claudeHostedWebTools(request *dto.ClaudeRequest, backend webtools.Backend) map[string]*hostedWebTool {
	tools := make(map[string]*hostedWebTool)
	served := false
	for _, tool := range request.GetTools() {
		definition, ok := claudeToolDefinition(tool)
		if !ok {
			continue
		}
		kind := claudeHostedToolKind(definition)
		if kind == "" {
			continue
		}
		name := common.Interface2String(definition["name"])
		if name == "" {
			name = kind
		}
		tools[name] = &hostedWebTool{
			kind:             kind,
			name:             name,
			maxUses:          claudeIntField(definition["max_uses"]),
			allowedDomains:   webToolStrings(definition["allowed_domains"]),
			blockedDomains:   webToolStrings(definition["blocked_domains"]),
			maxContentTokens: claudeIntField(definition["max_content_tokens"]),
		}
		served = served || backend.Serves(kind)
	}
	if len(tools) == 0 || !served {
		return nil
	}
	return tools
}

func claudeToolDefinition(tool any) (map[string]any, bool) {
	if definition, ok := tool.(map[string]any); ok {
		return definition, true
	}
	data, err := common.Marshal(tool)
	if err != nil {
		return nil, false
	}
	var definition map[string]any
	if err := common.Unmarshal(data, &definition); err != nil {
		return nil, false
	}
	return definition, true
}

func claudeHostedToolKind(definition map[string]any) string {
	toolType := common.Interface2String(definition["type"])
	switch {
	case strings.HasPrefix(toolType, "web_search_"):
		return webtools.ToolWebSearch
	case strings.HasPrefix(toolType, "web_fetch_"):
		return webtools.ToolWebFetch
	default:
		return ""
	}
}

func claudeIntField(value any) int {
	if number, ok := value.(float64); ok && number > 0 && number < 1e9 {
		return int(number)
	}
	return 0
}

// gatewayClaudeRequest returns a copy of the request in which the upstream
// calls the hosted web tools as functions and earlier gateway rounds of the
// history are function calls and results.
func gatewayClaudeRequest(request *dto.ClaudeRequest, tools map[string]*hostedWebTool) (*dto.ClaudeRequest, error) {
	upstream, err := cloneClaudeRequest(request)
	if err != nil {
		return nil, err
	}
	// Client tools stay raw so their schema property order reaches the
	// upstream; GetTools returns decoded copies, which would re-encode sorted.
	var clientTools []any
	if raw, ok := upstream.Tools.(json.RawMessage); ok {
		var items []json.RawMessage
		if err := common.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			clientTools = append(clientTools, item)
		}
	} else {
		clientTools = upstream.GetTools()
	}
	rewritten := make([]any, 0, len(clientTools))
	for _, tool := range clientTools {
		if definition, ok := claudeToolDefinition(tool); ok && claudeHostedToolKind(definition) != "" {
			name := common.Interface2String(definition["name"])
			if name == "" {
				name = claudeHostedToolKind(definition)
			}
			if hosted := tools[name]; hosted != nil {
				description, schema := webToolFunction(hosted.kind)
				rewritten = append(rewritten, map[string]any{"name": hosted.name, "description": description, "input_schema": schema})
				continue
			}
		}
		rewritten = append(rewritten, tool)
	}
	upstream.Tools = rewritten
	upstream.Messages = replayClaudeWebToolRounds(upstream.Messages, tools)
	return upstream, nil
}

func cloneClaudeRequest(request *dto.ClaudeRequest) (*dto.ClaudeRequest, error) {
	data, err := common.Marshal(request)
	if err != nil {
		return nil, err
	}
	var clone dto.ClaudeRequest
	if err := common.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

// replayClaudeWebToolRounds turns the gateway rounds of earlier assistant
// turns back into function calls and results: an assistant message is split
// after the blocks of each round, and the results open the next user message.
func replayClaudeWebToolRounds(messages []dto.ClaudeMessage, tools map[string]*hostedWebTool) []dto.ClaudeMessage {
	replayed := make([]dto.ClaudeMessage, 0, len(messages))
	var carried []any
	for _, message := range messages {
		blocks, isArray := message.Content.([]any)
		if len(carried) > 0 {
			if message.Role == "user" {
				if !isArray {
					blocks = []any{map[string]any{"type": "text", "text": message.GetStringContent()}}
				}
				message.Content = append(carried, blocks...)
				carried = nil
				replayed = append(replayed, message)
				continue
			}
			replayed = append(replayed, dto.ClaudeMessage{Role: "user", Content: carried})
			carried = nil
		}
		if message.Role != "assistant" || !isArray {
			replayed = append(replayed, message)
			continue
		}

		var current, results []any
		inputs := make(map[string]any)
		var unanswered []string
		for _, raw := range blocks {
			block, _ := raw.(map[string]any)
			blockType := common.Interface2String(block["type"])
			switch {
			case blockType == "server_tool_use" && tools[common.Interface2String(block["name"])] != nil:
				if len(results) > 0 {
					replayed = append(replayed, dto.ClaudeMessage{Role: "assistant", Content: current}, dto.ClaudeMessage{Role: "user", Content: results})
					current, results = nil, nil
				}
				id := strings.TrimPrefix(common.Interface2String(block["id"]), claudeServerToolIDPrefix)
				inputs[id] = block["input"]
				unanswered = append(unanswered, id)
				current = append(current, map[string]any{"type": "tool_use", "id": id, "name": block["name"], "input": block["input"]})
			case blockType == "web_search_tool_result" || blockType == "web_fetch_tool_result":
				id := strings.TrimPrefix(common.Interface2String(block["tool_use_id"]), claudeServerToolIDPrefix)
				input, converted := inputs[id]
				if !converted {
					current = append(current, raw)
					continue
				}
				delete(inputs, id)
				text, isError := claudeWebToolResultText(block, input)
				results = append(results, claudeToolResult(id, text, isError))
			default:
				if len(results) > 0 {
					replayed = append(replayed, dto.ClaudeMessage{Role: "assistant", Content: current}, dto.ClaudeMessage{Role: "user", Content: results})
					current, results = nil, nil
				}
				current = append(current, raw)
			}
		}
		// A call without a result, as in a paused turn, still needs one.
		for _, id := range unanswered {
			if _, open := inputs[id]; open {
				results = append(results, claudeToolResult(id, "Error: "+webtools.ErrorUnavailable, true))
			}
		}
		if len(current) > 0 {
			replayed = append(replayed, dto.ClaudeMessage{Role: "assistant", Content: current})
		}
		carried = results
	}
	if len(carried) > 0 {
		replayed = append(replayed, dto.ClaudeMessage{Role: "user", Content: carried})
	}
	return replayed
}

func claudeToolResult(id string, text string, isError bool) map[string]any {
	result := map[string]any{"type": "tool_result", "tool_use_id": id, "content": text}
	if isError {
		result["is_error"] = true
	}
	return result
}

type claudeSearchEnvelope struct {
	Title   string `json:"t"`
	URL     string `json:"u"`
	Summary string `json:"s,omitempty"`
	Date    string `json:"d,omitempty"`
}

// claudeWebToolResultText is the function result the upstream model reads for
// a gateway result block. Results are rebuilt from the block alone, so a
// replayed history reads exactly as the round that produced it.
func claudeWebToolResultText(block map[string]any, input any) (string, bool) {
	result := &webToolResult{kind: webtools.ToolWebSearch}
	content := block["content"]
	if object, ok := content.(map[string]any); ok {
		switch common.Interface2String(object["type"]) {
		case "web_fetch_result":
			document, _ := object["content"].(map[string]any)
			source, _ := document["source"].(map[string]any)
			result.kind = webtools.ToolWebFetch
			result.title = common.Interface2String(document["title"])
			result.url = common.Interface2String(object["url"])
			result.content = common.Interface2String(source["data"])
		case "web_search_tool_result_error", "web_fetch_tool_error":
			result.errorCode = cmp.Or(common.Interface2String(object["error_code"]), webtools.ErrorUnavailable)
		default:
			result.errorCode = webtools.ErrorUnavailable
		}
		return result.text(), result.errorCode != ""
	}

	arguments, _ := input.(map[string]any)
	result.query = common.Interface2String(arguments["query"])
	items, _ := content.([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		hit := claudeSearchEnvelope{
			Title: common.Interface2String(item["title"]),
			URL:   common.Interface2String(item["url"]),
		}
		if encoded, ok := strings.CutPrefix(common.Interface2String(item["encrypted_content"]), claudeSearchEnvelopePrefix); ok {
			if data, err := base64.RawURLEncoding.DecodeString(encoded); err == nil {
				_ = common.Unmarshal(data, &hit)
			}
		}
		result.hits = append(result.hits, webtools.SearchHit{Title: hit.Title, URL: hit.URL, Summary: hit.Summary, Date: hit.Date})
	}
	return result.text(), false
}

// claudeWebToolResultBlock presents the result of a gateway call as the
// result block of a server tool.
func claudeWebToolResultBlock(result *webToolResult, serverID string) map[string]any {
	if result.errorCode != "" {
		if result.kind == webtools.ToolWebFetch {
			return map[string]any{
				"type":        "web_fetch_tool_result",
				"tool_use_id": serverID,
				"content":     map[string]any{"type": "web_fetch_tool_error", "error_code": result.errorCode},
			}
		}
		return map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": serverID,
			"content":     map[string]any{"type": "web_search_tool_result_error", "error_code": result.errorCode},
		}
	}
	if result.kind == webtools.ToolWebFetch {
		return map[string]any{
			"type":        "web_fetch_tool_result",
			"tool_use_id": serverID,
			"content": map[string]any{
				"type":         "web_fetch_result",
				"url":          result.url,
				"retrieved_at": time.Now().UTC().Format(time.RFC3339),
				"content": map[string]any{
					"type":   "document",
					"source": map[string]any{"type": "text", "media_type": "text/plain", "data": result.content},
					"title":  result.title,
				},
			},
		}
	}
	results := make([]any, 0, len(result.hits))
	for _, hit := range result.hits {
		envelope, _ := common.Marshal(claudeSearchEnvelope{Title: hit.Title, URL: hit.URL, Summary: hit.Summary, Date: hit.Date})
		item := map[string]any{
			"type":              "web_search_result",
			"title":             hit.Title,
			"url":               hit.URL,
			"encrypted_content": claudeSearchEnvelopePrefix + base64.RawURLEncoding.EncodeToString(envelope),
		}
		if date, _, _ := strings.Cut(hit.Date, "T"); date != "" {
			item["page_age"] = date
		}
		results = append(results, item)
	}
	return map[string]any{"type": "web_search_tool_result", "tool_use_id": serverID, "content": results}
}

// relayClaudeWithWebTools relays a Messages request whose hosted web tools
// the gateway runs, and returns the usage of all upstream rounds.
func relayClaudeWithWebTools(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor, request *dto.ClaudeRequest, tools map[string]*hostedWebTool, backend webtools.Backend) (*dto.Usage, *types.NewAPIError) {
	upstream, err := gatewayClaudeRequest(request, tools)
	if err != nil {
		return nil, newConvertRequestFailedError(c, info, err)
	}
	run := newWebToolsRun(backend, operation_setting.GetWebToolsSetting())
	clientStream := info.IsStream
	upstreamModelName, reasoningEffort := info.UpstreamModelName, info.ReasoningEffort
	turn := &claudeWebToolsTurn{writer: &webToolsStream{client: c.Writer}, stream: clientStream, tools: tools}
	defer func() {
		c.Writer = turn.writer.client
		info.IsStream = clientStream
	}()

	usage := &dto.Usage{}
	clientUsage := &dto.ClaudeUsage{}
	var first, last *claudeRound
	stopReason := ""
	maxRounds := run.callLimit + 1
	for round := 0; ; round++ {
		if round > 0 {
			resetWebToolsRound(info, upstreamModelName, reasoningEffort)
		}
		info.IsStream = clientStream
		roundRequest, err := cloneClaudeRequest(upstream)
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
		if result.usage != nil {
			clientUsage.InputTokens += result.usage.InputTokens
			clientUsage.OutputTokens += result.usage.OutputTokens
			clientUsage.CacheCreationInputTokens += result.usage.CacheCreationInputTokens
			clientUsage.CacheReadInputTokens += result.usage.CacheReadInputTokens
		}
		if first == nil {
			first = result
		}
		last = result
		stopReason = result.stopReason

		var calls []map[string]any
		clientCalls := false
		for _, block := range result.content() {
			if common.Interface2String(block["type"]) != "tool_use" {
				continue
			}
			if tools[common.Interface2String(block["name"])] != nil {
				calls = append(calls, block)
			} else {
				clientCalls = true
			}
		}
		if len(calls) == 0 {
			break
		}
		results := make([]any, 0, len(calls))
		for _, call := range calls {
			id := common.Interface2String(call["id"])
			input, _ := call["input"].(map[string]any)
			block := claudeWebToolResultBlock(run.call(c, tools[common.Interface2String(call["name"])], input), claudeServerToolIDPrefix+id)
			turn.emitBlock(block)
			text, isError := claudeWebToolResultText(block, input)
			results = append(results, claudeToolResult(id, text, isError))
		}
		if clientCalls {
			stopReason = "tool_use"
			break
		}
		if result.stopReason == "max_tokens" || round+1 >= maxRounds || c.Request.Context().Err() != nil {
			stopReason = result.stopReason
			if stopReason == "tool_use" {
				stopReason = "end_turn"
			}
			break
		}
		upstream.Messages = append(upstream.Messages,
			dto.ClaudeMessage{Role: "assistant", Content: result.upstreamContent()},
			dto.ClaudeMessage{Role: "user", Content: results},
		)
		// A forced hosted tool is called once; later rounds choose freely.
		if choice, ok := upstream.ToolChoice.(map[string]any); ok && tools[common.Interface2String(choice["name"])] != nil {
			upstream.ToolChoice = map[string]any{"type": "auto"}
		}
	}

	if stopReason == "" {
		stopReason = "end_turn"
	}
	finalUsage := map[string]any{
		"input_tokens":                clientUsage.InputTokens,
		"cache_creation_input_tokens": clientUsage.CacheCreationInputTokens,
		"cache_read_input_tokens":     clientUsage.CacheReadInputTokens,
		"output_tokens":               clientUsage.OutputTokens,
		"server_tool_use": map[string]any{
			"web_search_requests": run.billable[webtools.ToolWebSearch],
			"web_fetch_requests":  run.billable[webtools.ToolWebFetch],
		},
	}
	if err := turn.finish(c, first, last, stopReason, finalUsage); err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	run.bill(info)
	return usage, nil
}

// claudeRound is one upstream response in the client's Messages format.
type claudeRound struct {
	message      map[string]any
	blocks       map[int]map[string]any
	order        []int
	inputs       map[int]*strings.Builder
	offset       int
	stopReason   string
	stopSequence any
	usage        *dto.ClaudeUsage
}

func newClaudeRound(offset int) *claudeRound {
	return &claudeRound{blocks: make(map[int]map[string]any), inputs: make(map[int]*strings.Builder), offset: offset}
}

// apply adds one Messages stream event to the round.
func (r *claudeRound) apply(event map[string]any) {
	index := jsonInt(event["index"])
	switch common.Interface2String(event["type"]) {
	case "message_start":
		r.message, _ = event["message"].(map[string]any)
		r.mergeUsage(r.message["usage"])
	case "content_block_start":
		source, _ := event["content_block"].(map[string]any)
		block := make(map[string]any, len(source))
		maps.Copy(block, source)
		if _, exists := r.blocks[index]; !exists {
			r.order = append(r.order, index)
		}
		r.blocks[index] = block
		switch common.Interface2String(block["type"]) {
		case "tool_use", "server_tool_use":
			r.inputs[index] = &strings.Builder{}
		}
	case "content_block_delta":
		block := r.blocks[index]
		delta, _ := event["delta"].(map[string]any)
		if block == nil || delta == nil {
			return
		}
		switch common.Interface2String(delta["type"]) {
		case "text_delta":
			block["text"] = common.Interface2String(block["text"]) + common.Interface2String(delta["text"])
		case "input_json_delta":
			if input := r.inputs[index]; input != nil {
				input.WriteString(common.Interface2String(delta["partial_json"]))
			}
		case "thinking_delta":
			block["thinking"] = common.Interface2String(block["thinking"]) + common.Interface2String(delta["thinking"])
		case "signature_delta":
			block["signature"] = delta["signature"]
		case "citations_delta":
			citations, _ := block["citations"].([]any)
			block["citations"] = append(citations, delta["citation"])
		}
	case "content_block_stop":
		block := r.blocks[index]
		input := r.inputs[index]
		if block == nil || input == nil {
			return
		}
		if input.Len() > 0 {
			var value any
			if err := common.UnmarshalJsonStr(input.String(), &value); err == nil {
				block["input"] = value
			}
		}
		if block["input"] == nil {
			block["input"] = map[string]any{}
		}
	case "message_delta":
		if delta, ok := event["delta"].(map[string]any); ok {
			r.stopReason = common.Interface2String(delta["stop_reason"])
			r.stopSequence = delta["stop_sequence"]
		}
		r.mergeUsage(event["usage"])
	}
}

// applyMessage takes a whole non-stream Messages response.
func (r *claudeRound) applyMessage(message map[string]any) {
	r.message = message
	content, _ := message["content"].([]any)
	for i, raw := range content {
		if block, ok := raw.(map[string]any); ok {
			r.blocks[i] = block
			r.order = append(r.order, i)
		}
	}
	r.stopReason = common.Interface2String(message["stop_reason"])
	r.stopSequence = message["stop_sequence"]
	r.mergeUsage(message["usage"])
}

func (r *claudeRound) mergeUsage(value any) {
	if value == nil {
		return
	}
	data, err := common.Marshal(value)
	if err != nil {
		return
	}
	var usage dto.ClaudeUsage
	if err := common.Unmarshal(data, &usage); err != nil {
		return
	}
	r.usage = dto.MergeClaudeUsageNonZero(r.usage, &usage)
}

func (r *claudeRound) content() []map[string]any {
	blocks := make([]map[string]any, 0, len(r.order))
	for _, index := range r.order {
		blocks = append(blocks, r.blocks[index])
	}
	return blocks
}

// upstreamContent is the round as the next upstream request replays it.
// Empty text blocks are dropped because Messages requests reject them.
func (r *claudeRound) upstreamContent() []any {
	content := make([]any, 0, len(r.order))
	for _, block := range r.content() {
		if common.Interface2String(block["type"]) == "text" && common.Interface2String(block["text"]) == "" {
			continue
		}
		content = append(content, block)
	}
	return content
}

// claudeWebToolsTurn presents the upstream rounds of one request to the
// client as a single Messages response. A streaming client receives each
// round's events as they arrive, renumbered into one sequence of content
// blocks.
type claudeWebToolsTurn struct {
	writer    *webToolsStream
	stream    bool
	tools     map[string]*hostedWebTool
	mu        sync.Mutex
	started   bool
	nextIndex int
	content   []any
}

// relay sends one round through the normal Messages path.
func (t *claudeWebToolsTurn) relay(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor, request *dto.ClaudeRequest) (*claudeRound, *dto.Usage, *types.NewAPIError) {
	round := newClaudeRound(t.nextIndex)
	writer := newWebToolsRoundWriter(t.writer, t.stream, func(event map[string]any) {
		round.apply(event)
		if t.stream {
			t.forward(round, event)
		}
	})
	c.Writer = writer
	usage, apiErr := relayClaudeMessages(c, info, adaptor, request)
	c.Writer = t.writer.client
	if apiErr != nil {
		return nil, nil, apiErr
	}
	message, err := writer.complete()
	if err != nil {
		return nil, nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if message != nil {
		round.applyMessage(message)
	}
	if !t.stream {
		for _, block := range round.content() {
			t.content = append(t.content, t.clientBlock(block))
		}
	}
	return round, usage, nil
}

// clientBlock shows a gateway function call as a server tool call.
func (t *claudeWebToolsTurn) clientBlock(block map[string]any) map[string]any {
	if common.Interface2String(block["type"]) != "tool_use" || t.tools[common.Interface2String(block["name"])] == nil {
		return block
	}
	shown := make(map[string]any, len(block))
	maps.Copy(shown, block)
	shown["type"] = "server_tool_use"
	shown["id"] = claudeServerToolIDPrefix + common.Interface2String(block["id"])
	return shown
}

// forward writes one live upstream event to a streaming client.
func (t *claudeWebToolsTurn) forward(round *claudeRound, event map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	eventType := common.Interface2String(event["type"])
	switch eventType {
	case "message_start":
		if !t.started {
			t.start(round.message)
		}
	case "content_block_start", "content_block_delta", "content_block_stop":
		if !t.started {
			t.start(nil)
		}
		index := round.offset + jsonInt(event["index"])
		shown := make(map[string]any, len(event))
		maps.Copy(shown, event)
		shown["index"] = index
		if block, ok := event["content_block"].(map[string]any); ok {
			shown["content_block"] = t.clientBlock(block)
		}
		t.writer.writeEvent(eventType, shown)
		t.nextIndex = max(t.nextIndex, index+1)
	case "ping":
		t.writer.writeEvent(eventType, event)
	}
}

// start writes message_start, from the first round's message when known.
func (t *claudeWebToolsTurn) start(message map[string]any) {
	shown := map[string]any{"type": "message", "role": "assistant", "stop_reason": nil, "stop_sequence": nil}
	maps.Copy(shown, message)
	shown["content"] = []any{}
	t.started = true
	t.writer.writeEvent("message_start", map[string]any{"type": "message_start", "message": shown})
}

// emitBlock presents a complete block, such as a gateway call result.
func (t *claudeWebToolsTurn) emitBlock(block map[string]any) {
	if !t.stream {
		t.content = append(t.content, block)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		t.start(nil)
	}
	index := t.nextIndex
	t.nextIndex++
	t.writer.writeEvent("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": block})
	t.writer.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
}

// finish ends the response with the stop reason of the turn and the usage of
// all rounds.
func (t *claudeWebToolsTurn) finish(c *gin.Context, first *claudeRound, last *claudeRound, stopReason string, usage map[string]any) error {
	if t.stream {
		t.mu.Lock()
		defer t.mu.Unlock()
		if !t.started {
			t.start(first.message)
		}
		t.writer.writeEvent("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": last.stopSequence},
			"usage": usage,
		})
		t.writer.writeEvent("message_stop", map[string]any{"type": "message_stop"})
		return nil
	}
	message := map[string]any{"type": "message", "role": "assistant"}
	maps.Copy(message, first.message)
	message["content"] = t.content
	message["stop_reason"] = stopReason
	message["stop_sequence"] = last.stopSequence
	message["usage"] = usage
	data, err := common.Marshal(message)
	if err != nil {
		return err
	}
	c.Data(http.StatusOK, "application/json", data)
	return nil
}
