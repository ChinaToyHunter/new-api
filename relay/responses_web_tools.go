package relay

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/webtools"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// Gateway web tools on the Responses path (see web_tools.go).
//
// The upstream calls web search and fetch functions in place of the hosted
// web_search tool, and the client sees each call as a web_search_call item: a
// search action for a search, an open_page action for a fetch. Clients send
// those items back without results, so a later request replays them as
// function calls whose output says the results are no longer kept, which is
// what a hosted search leaves the model in later turns as well.

// responsesHostedWebTools returns the gateway functions standing in for the
// hosted web_search tool of a request, keyed by name, or nil when the gateway
// does not take it over.
func responsesHostedWebTools(request *dto.OpenAIResponsesRequest, backend webtools.Backend) map[string]*hostedWebTool {
	var hosted map[string]any
	taken := make(map[string]bool)
	for _, tool := range request.GetToolsMap() {
		if strings.HasPrefix(common.Interface2String(tool["type"]), "web_search") {
			if hosted == nil {
				hosted = tool
			}
			continue
		}
		taken[common.Interface2String(tool["name"])] = true
	}
	if hosted == nil {
		return nil
	}
	filters, _ := hosted["filters"].(map[string]any)
	return gatewayWebTools(taken, backend, webToolStrings(filters["allowed_domains"]))
}

// gatewayResponsesRequest returns a copy of the request in which the upstream
// calls the gateway functions in place of the hosted web search, and the
// web_search_call items of the input are function calls and outputs.
func gatewayResponsesRequest(request *dto.OpenAIResponsesRequest, tools map[string]*hostedWebTool) (*dto.OpenAIResponsesRequest, error) {
	upstream, err := common.DeepCopy(request)
	if err != nil {
		return nil, err
	}
	var definitions []json.RawMessage
	if err := common.Unmarshal(upstream.Tools, &definitions); err != nil {
		return nil, err
	}
	rewritten := make([]any, 0, len(definitions)+1)
	replaced := false
	for _, definition := range definitions {
		var tool struct {
			Type string `json:"type"`
		}
		_ = common.Unmarshal(definition, &tool)
		if !strings.HasPrefix(tool.Type, "web_search") {
			rewritten = append(rewritten, definition)
			continue
		}
		if replaced {
			continue
		}
		replaced = true
		for _, hosted := range orderedWebTools(tools) {
			description, parameters := webToolFunction(hosted.kind)
			rewritten = append(rewritten, map[string]any{"type": "function", "name": hosted.name, "description": description, "parameters": parameters, "strict": false})
		}
	}
	if upstream.Tools, err = common.Marshal(rewritten); err != nil {
		return nil, err
	}

	var choice struct {
		Type string `json:"type"`
	}
	if len(upstream.ToolChoice) > 0 && common.Unmarshal(upstream.ToolChoice, &choice) == nil && strings.HasPrefix(choice.Type, "web_search") {
		search := webToolOfKind(tools, webtools.ToolWebSearch)
		if upstream.ToolChoice, err = common.Marshal(map[string]any{"type": "function", "name": search.name}); err != nil {
			return nil, err
		}
	}

	// The upstream runs no hosted search, so it has nothing of one to include.
	var include []string
	if len(upstream.Include) > 0 && common.Unmarshal(upstream.Include, &include) == nil {
		include = slices.DeleteFunc(include, func(field string) bool { return strings.HasPrefix(field, "web_search_call.") })
		if upstream.Include, err = common.Marshal(include); err != nil {
			return nil, err
		}
	}

	if upstream.Input, err = replayResponsesWebToolCalls(upstream.Input, tools); err != nil {
		return nil, err
	}
	return upstream, nil
}

// replayResponsesWebToolCalls turns the web_search_call items of an input
// into calls of the gateway functions. Clients keep no results with the
// items, so each output says the results are no longer available. An input
// without such items is returned unchanged.
func replayResponsesWebToolCalls(input json.RawMessage, tools map[string]*hostedWebTool) (json.RawMessage, error) {
	if common.GetJsonType(input) != "array" {
		return input, nil
	}
	var items []json.RawMessage
	if err := common.Unmarshal(input, &items); err != nil {
		return nil, err
	}
	search, fetch := webToolOfKind(tools, webtools.ToolWebSearch), webToolOfKind(tools, webtools.ToolWebFetch)
	replayed := make([]any, 0, len(items))
	changed := false
	for index, raw := range items {
		var item struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Action struct {
				Type    string   `json:"type"`
				Query   string   `json:"query"`
				Queries []string `json:"queries"`
				URL     string   `json:"url"`
				Sources []struct {
					URL string `json:"url"`
				} `json:"sources"`
			} `json:"action"`
		}
		if err := common.Unmarshal(raw, &item); err != nil || item.Type != "web_search_call" {
			replayed = append(replayed, raw)
			continue
		}
		changed = true
		var name, output string
		var arguments map[string]any
		switch item.Action.Type {
		case "open_page", "find_in_page":
			if fetch == nil || item.Action.URL == "" {
				continue
			}
			name, arguments = fetch.name, map[string]any{"url": item.Action.URL}
			output = fmt.Sprintf("The content of %s was returned in an earlier turn and is no longer available. Fetch it again if it is needed.", item.Action.URL)
		default:
			query := cmp.Or(item.Action.Query, strings.Join(item.Action.Queries, " "))
			if query == "" {
				continue
			}
			name, arguments = search.name, map[string]any{"query": query}
			var text strings.Builder
			fmt.Fprintf(&text, "Search results for %q were returned in an earlier turn and are no longer available. Search again if they are needed.", query)
			for i, source := range item.Action.Sources {
				if i == 0 {
					text.WriteString("\n\nSources:")
				}
				text.WriteString("\n- ")
				text.WriteString(source.URL)
			}
			output = text.String()
		}
		if item.Status == "failed" {
			output = "Error: " + webtools.ErrorUnavailable
		}
		encoded, err := common.Marshal(arguments)
		if err != nil {
			return nil, err
		}
		callID := fmt.Sprintf("call_web_%d", index)
		replayed = append(replayed,
			map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": string(encoded)},
			map[string]any{"type": "function_call_output", "call_id": callID, "output": output},
		)
	}
	if !changed {
		return input, nil
	}
	return common.Marshal(replayed)
}

// relayResponsesWithWebTools relays a Responses request whose hosted web
// search the gateway runs, and returns the usage of all upstream rounds.
func relayResponsesWithWebTools(c *gin.Context, info *relaycommon.RelayInfo, request *dto.OpenAIResponsesRequest, tools map[string]*hostedWebTool, backend webtools.Backend) (*dto.Usage, *types.NewAPIError) {
	upstream, err := gatewayResponsesRequest(request, tools)
	if err != nil {
		return nil, newConvertRequestFailedError(c, info, err)
	}
	run := newWebToolsRun(backend, operation_setting.GetWebToolsSetting())
	if request.MaxToolCalls != nil && *request.MaxToolCalls < uint(run.callLimit) {
		run.callLimit = int(*request.MaxToolCalls)
	}
	// Later rounds send the input as items, a text input as one user message.
	var items []any
	switch common.GetJsonType(upstream.Input) {
	case "array":
		var raws []json.RawMessage
		if err := common.Unmarshal(upstream.Input, &raws); err != nil {
			return nil, newConvertRequestFailedError(c, info, err)
		}
		for _, raw := range raws {
			items = append(items, raw)
		}
	case "string":
		var text string
		if err := common.Unmarshal(upstream.Input, &text); err != nil {
			return nil, newConvertRequestFailedError(c, info, err)
		}
		items = append(items, map[string]any{"type": "message", "role": "user", "content": text})
	}
	var include []string
	_ = common.Unmarshal(request.Include, &include)

	clientStream := info.IsStream
	upstreamModelName, reasoningEffort := info.UpstreamModelName, info.ReasoningEffort
	turn := &responsesWebToolsTurn{
		writer:         &webToolsStream{client: c.Writer},
		stream:         clientStream,
		tools:          tools,
		includeSources: slices.Contains(include, "web_search_call.action.sources"),
		output:         make(map[int]any),
		usage:          make(map[string]any),
	}
	defer func() {
		c.Writer = turn.writer.client
		info.IsStream = clientStream
	}()

	usage := &dto.Usage{}
	var last *responsesRound
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
		result, roundUsage, apiErr := turn.relay(c, info, roundRequest)
		if apiErr != nil {
			if turn.started {
				// The client already holds part of the response.
				types.ErrOptionWithSkipRetry()(apiErr)
			}
			return nil, apiErr
		}
		addWebToolsRoundUsage(usage, roundUsage)
		last = result

		calls, clientCalls := result.gatewayCalls()
		// A call the client runs ends the turn. Gateway calls beside it are
		// dropped unrun; the model can make them again with the client's
		// results in hand.
		if len(calls) == 0 || clientCalls || cmp.Or(result.status, "completed") != "completed" {
			break
		}
		outputs := make([]any, 0, len(calls))
		for _, call := range calls {
			index, id := turn.beginCall()
			webResult := run.call(c, tools[common.Interface2String(call["name"])], webToolArguments(call["arguments"]))
			turn.endCall(index, id, webResult)
			outputs = append(outputs, map[string]any{"type": "function_call_output", "call_id": call["call_id"], "output": webResult.text()})
		}
		if round+1 >= maxRounds || c.Request.Context().Err() != nil {
			break
		}
		items = append(items, result.upstreamItems()...)
		items = append(items, outputs...)
		if upstream.Input, err = common.Marshal(items); err != nil {
			return nil, newConvertRequestFailedError(c, info, err)
		}
		// A forced call is made once; later rounds choose freely.
		var choice any
		if len(upstream.ToolChoice) > 0 && common.Unmarshal(upstream.ToolChoice, &choice) == nil {
			forced, _ := choice.(map[string]any)
			if choice == "required" || (forced != nil && tools[common.Interface2String(forced["name"])] != nil) {
				upstream.ToolChoice = json.RawMessage(`"auto"`)
			}
		}
	}

	if err := turn.finish(c, last); err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	run.bill(info)
	return usage, nil
}

// isResponsesClientCall reports whether an output item asks the client to run
// a tool. Tools the upstream runs itself do not.
func isResponsesClientCall(itemType string) bool {
	switch itemType {
	case "web_search_call", "file_search_call", "image_generation_call", "code_interpreter_call", "mcp_call", "mcp_list_tools":
		return false
	case "mcp_approval_request":
		return true
	}
	return strings.HasSuffix(itemType, "_call")
}

// responsesRound is one upstream response in the client's Responses format.
type responsesRound struct {
	tools    map[string]*hostedWebTool
	response map[string]any
	items    map[int]map[string]any
	order    []int
	// gateway marks the items that call a gateway function; shown maps the
	// other items to their output index in the client's response.
	gateway map[int]bool
	shown   map[int]int
	status  string
}

func newResponsesRound(tools map[string]*hostedWebTool) *responsesRound {
	return &responsesRound{
		tools:   tools,
		items:   make(map[int]map[string]any),
		gateway: make(map[int]bool),
		shown:   make(map[int]int),
	}
}

func (r *responsesRound) addItem(index int, item map[string]any) {
	if _, exists := r.items[index]; !exists {
		r.order = append(r.order, index)
		r.gateway[index] = common.Interface2String(item["type"]) == "function_call" && r.tools[common.Interface2String(item["name"])] != nil
	}
	r.items[index] = item
}

// apply adds one Responses stream event to the round.
func (r *responsesRound) apply(event map[string]any) {
	eventType := common.Interface2String(event["type"])
	switch eventType {
	case "response.created", "response.in_progress", "response.queued":
		if response, ok := event["response"].(map[string]any); ok {
			r.response = response
		}
	case "response.output_item.added", "response.output_item.done":
		if item, ok := event["item"].(map[string]any); ok {
			r.addItem(jsonInt(event["output_index"]), item)
		}
	case "response.completed", "response.incomplete", "response.failed":
		response, _ := event["response"].(map[string]any)
		r.applyResponse(response)
		r.status = strings.TrimPrefix(eventType, "response.")
	case "error":
		r.status = "failed"
	}
}

// applyResponse takes a whole response object: a non-stream response, or the
// one a stream ends with. Its output stands in for items a stream did not
// show.
func (r *responsesRound) applyResponse(response map[string]any) {
	if response == nil {
		return
	}
	r.response = response
	r.status = common.Interface2String(response["status"])
	if len(r.items) > 0 {
		return
	}
	output, _ := response["output"].([]any)
	for index, raw := range output {
		if item, ok := raw.(map[string]any); ok {
			r.addItem(index, item)
		}
	}
}

// gatewayCalls returns the round's calls of gateway functions, and whether
// the round also calls a tool the client runs.
func (r *responsesRound) gatewayCalls() ([]map[string]any, bool) {
	var calls []map[string]any
	clientCalls := false
	for _, index := range r.order {
		item := r.items[index]
		switch {
		case r.gateway[index]:
			calls = append(calls, item)
		case isResponsesClientCall(common.Interface2String(item["type"])):
			clientCalls = true
		}
	}
	return calls, clientCalls
}

// upstreamItems are the round's output items as the next round's input,
// carried inline as Codex replays them: an upstream that stores nothing
// cannot resolve item IDs, so they are dropped, together with reasoning items
// that carry no encrypted content.
func (r *responsesRound) upstreamItems() []any {
	items := make([]any, 0, len(r.order))
	for _, index := range r.order {
		item := r.items[index]
		if common.Interface2String(item["type"]) == "reasoning" && common.Interface2String(item["encrypted_content"]) == "" {
			continue
		}
		replayed := maps.Clone(item)
		delete(replayed, "id")
		items = append(items, replayed)
	}
	return items
}

// responsesWebToolsTurn presents the upstream rounds of one request to the
// client as a single response. A streaming client receives each round's
// events as they arrive, renumbered into one sequence of output items.
type responsesWebToolsTurn struct {
	writer         *webToolsStream
	stream         bool
	tools          map[string]*hostedWebTool
	includeSources bool
	mu             sync.Mutex
	started        bool
	// response is the first round's response object, whose identity the
	// client keeps for the whole turn.
	response  map[string]any
	sequence  int
	nextIndex int
	output    map[int]any
	usage     map[string]any
}

// relay sends one round through the normal Responses path.
func (t *responsesWebToolsTurn) relay(c *gin.Context, info *relaycommon.RelayInfo, request *dto.OpenAIResponsesRequest) (*responsesRound, *dto.Usage, *types.NewAPIError) {
	round := newResponsesRound(t.tools)
	writer := newWebToolsRoundWriter(t.writer, t.stream, func(event map[string]any) {
		round.apply(event)
		if t.stream {
			t.forward(round, event)
		}
	})
	c.Writer = writer
	usage, apiErr := relayResponses(c, info, request)
	c.Writer = t.writer.client
	if apiErr != nil {
		return nil, nil, apiErr
	}
	response, err := writer.complete()
	if err != nil {
		return nil, nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	round.applyResponse(response)
	if roundUsage, ok := round.response["usage"].(map[string]any); ok {
		addUsageFields(t.usage, roundUsage)
	}
	if !t.stream {
		if t.response == nil {
			t.response = round.response
		}
		for _, index := range round.order {
			if !round.gateway[index] {
				t.output[t.nextIndex] = round.items[index]
				t.nextIndex++
			}
		}
	}
	return round, usage, nil
}

// forward writes one live upstream event to a streaming client.
func (t *responsesWebToolsTurn) forward(round *responsesRound, event map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	eventType := common.Interface2String(event["type"])
	switch eventType {
	case "response.created", "response.in_progress", "response.queued",
		"response.completed", "response.incomplete", "response.failed":
		// The turn opens and ends the response once.
		return
	}
	if !t.started {
		t.start(round.response)
	}
	if _, ok := event["output_index"]; !ok {
		t.writeEvent(event)
		return
	}
	index := jsonInt(event["output_index"])
	if round.gateway[index] {
		return
	}
	shown, mapped := round.shown[index]
	if !mapped {
		shown = t.nextIndex
		t.nextIndex++
		round.shown[index] = shown
	}
	event = maps.Clone(event)
	event["output_index"] = shown
	if eventType == "response.output_item.done" {
		t.output[shown] = event["item"]
	}
	t.writeEvent(event)
}

// start opens the response, from the first round's response object when
// known.
func (t *responsesWebToolsTurn) start(response map[string]any) {
	t.started = true
	t.response = response
	shown := map[string]any{"object": "response", "status": "in_progress"}
	maps.Copy(shown, response)
	shown["output"] = []any{}
	t.writeEvent(map[string]any{"type": "response.created", "response": shown})
	t.writeEvent(map[string]any{"type": "response.in_progress", "response": shown})
}

func (t *responsesWebToolsTurn) writeEvent(event map[string]any) {
	event["sequence_number"] = t.sequence
	t.sequence++
	t.writer.writeEvent(common.Interface2String(event["type"]), event)
}

// beginCall shows a gateway call starting, as a web_search_call item.
func (t *responsesWebToolsTurn) beginCall() (int, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	index := t.nextIndex
	t.nextIndex++
	id := "ws_" + common.GetUUID()
	if !t.stream {
		return index, id
	}
	if !t.started {
		t.start(nil)
	}
	t.writeEvent(map[string]any{"type": "response.output_item.added", "output_index": index, "item": map[string]any{"type": "web_search_call", "id": id, "status": "in_progress"}})
	t.writeEvent(map[string]any{"type": "response.web_search_call.in_progress", "output_index": index, "item_id": id})
	t.writeEvent(map[string]any{"type": "response.web_search_call.searching", "output_index": index, "item_id": id})
	return index, id
}

// endCall shows the result of a gateway call.
func (t *responsesWebToolsTurn) endCall(index int, id string, result *webToolResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	action := map[string]any{"type": "search", "query": strings.TrimSpace(result.query)}
	if result.kind == webtools.ToolWebFetch {
		action = map[string]any{"type": "open_page", "url": result.url}
	} else if t.includeSources && len(result.hits) > 0 {
		sources := make([]any, 0, len(result.hits))
		for _, hit := range result.hits {
			sources = append(sources, map[string]any{"type": "url", "url": hit.URL})
		}
		action["sources"] = sources
	}
	item := map[string]any{"type": "web_search_call", "id": id, "status": "completed", "action": action}
	if result.errorCode != "" {
		item["status"] = "failed"
	}
	t.output[index] = item
	if !t.stream {
		return
	}
	if result.errorCode == "" {
		t.writeEvent(map[string]any{"type": "response.web_search_call.completed", "output_index": index, "item_id": id})
	}
	t.writeEvent(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
}

// finish ends the response with the status of the last round, the output of
// all rounds and their usage.
func (t *responsesWebToolsTurn) finish(c *gin.Context, last *responsesRound) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	response := map[string]any{"object": "response"}
	maps.Copy(response, last.response)
	for _, key := range []string{"id", "created_at"} {
		if value, ok := t.response[key]; ok {
			response[key] = value
		}
	}
	output := make([]any, 0, len(t.output))
	for index := range t.nextIndex {
		if item, ok := t.output[index]; ok {
			output = append(output, item)
		}
	}
	response["output"] = output
	if len(t.usage) > 0 {
		response["usage"] = t.usage
	}
	status := cmp.Or(last.status, "completed")
	response["status"] = status
	if !t.stream {
		data, err := common.Marshal(response)
		if err != nil {
			return err
		}
		c.Data(http.StatusOK, "application/json", data)
		return nil
	}
	if !t.started {
		t.start(last.response)
	}
	eventType := "response.completed"
	if status == "incomplete" || status == "failed" {
		eventType = "response." + status
	}
	t.writeEvent(map[string]any{"type": eventType, "response": response})
	return nil
}
