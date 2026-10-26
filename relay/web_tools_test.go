package relay

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/webtools"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWebToolsBackend struct {
	serves   bool
	hits     []webtools.SearchHit
	searches []string
}

// Serves offers search only, so the gateway adds no fetch function.
func (b *fakeWebToolsBackend) Serves(tool string) bool {
	return b.serves && tool == webtools.ToolWebSearch
}

func (b *fakeWebToolsBackend) Search(_ *gin.Context, query string, _ int) ([]webtools.SearchHit, error) {
	b.searches = append(b.searches, query)
	return b.hits, nil
}

func (b *fakeWebToolsBackend) Fetch(*gin.Context, string) (*webtools.Page, error) {
	return nil, &webtools.ToolError{Code: webtools.ErrorUnavailable}
}

// webToolsUpstream records the requests of an upstream model that answers
// each round with reply.
type webToolsUpstream struct {
	mu       sync.Mutex
	requests []map[string]any
	bodies   []string
	server   *httptest.Server
}

func newWebToolsUpstream(t *testing.T, reply func(w http.ResponseWriter, round int, stream bool)) *webToolsUpstream {
	upstream := &webToolsUpstream{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) || !assert.NoError(t, common.Unmarshal(body, &request)) {
			return
		}
		upstream.mu.Lock()
		upstream.requests = append(upstream.requests, request)
		upstream.bodies = append(upstream.bodies, string(body))
		round := len(upstream.requests)
		upstream.mu.Unlock()
		stream, _ := request["stream"].(bool)
		reply(w, round, stream)
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

// writeSSE writes named events, as Messages and Responses streams carry them.
func writeSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		var parsed map[string]any
		_ = common.UnmarshalJsonStr(event, &parsed)
		_, _ = io.WriteString(w, "event: "+common.Interface2String(parsed["type"])+"\ndata: "+event+"\n\n")
	}
}

// writeChatSSE writes Chat Completions chunks and the closing [DONE].
func writeChatSSE(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range chunks {
		_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

type sseEvent struct {
	name string
	data map[string]any
}

// readSSE returns the events of a client stream and its count of [DONE].
func readSSE(t *testing.T, body string) ([]sseEvent, int) {
	var events []sseEvent
	done := 0
	name := ""
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			name = value
		}
		value, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if value == "[DONE]" {
			done++
			continue
		}
		var data map[string]any
		require.NoError(t, common.UnmarshalJsonStr(value, &data))
		events = append(events, sseEvent{name: name, data: data})
	}
	return events, done
}

func newWebToolsContext(path string, channelType int, baseURL string) (*httptest.ResponseRecorder, *gin.Context) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "test-model")
	common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, baseURL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "test-key")
	return recorder, c
}

func useStreamingTimeout(t *testing.T) {
	previous := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previous })
}

func TestWebToolsRunEnforcesMaxUses(t *testing.T) {
	tool := &hostedWebTool{kind: webtools.ToolWebSearch, name: "web_search", maxUses: 1}
	run := newWebToolsRun(&fakeWebToolsBackend{serves: true, hits: []webtools.SearchHit{{Title: "Go", URL: "https://go.dev/"}}}, &operation_setting.WebToolsSetting{})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	first := run.call(c, tool, map[string]any{"query": "go"})
	assert.Empty(t, first.errorCode)
	second := run.call(c, tool, map[string]any{"query": "go"})
	assert.Equal(t, "Error: max_uses_exceeded", second.text())
	assert.Equal(t, map[string]any{"type": "web_search_tool_result_error", "error_code": webtools.ErrorMaxUsesExceeded}, claudeWebToolResultBlock(second, "srvtoolu_1")["content"])
	assert.Equal(t, 1, run.billable[webtools.ToolWebSearch])
}

// claudeWebToolsReply serves a model that searches once and then answers.
func claudeWebToolsReply(w http.ResponseWriter, round int, stream bool) {
	if round == 1 {
		if stream {
			writeSSE(w,
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"upstream-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me search."}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"web_search","input":{}}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"go release\"}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":12}}`,
				`{"type":"message_stop"}`,
			)
			return
		}
		writeJSON(w, `{"id":"msg_1","type":"message","role":"assistant","model":"upstream-model","content":[{"type":"text","text":"Let me search."},{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"go release"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":12}}`)
		return
	}
	if stream {
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"upstream-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":30,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Go was released."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		)
		return
	}
	writeJSON(w, `{"id":"msg_2","type":"message","role":"assistant","model":"upstream-model","content":[{"type":"text","text":"Go was released."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":30,"output_tokens":5}}`)
}

func newClaudeWebToolsRequest(stream bool) *dto.ClaudeRequest {
	maxTokens := uint(1024)
	return &dto.ClaudeRequest{
		Model:     "test-model",
		MaxTokens: &maxTokens,
		Stream:    &stream,
		Messages:  []dto.ClaudeMessage{{Role: "user", Content: "When was Go released?"}},
		Tools: []any{
			map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": float64(5)},
			map[string]any{"name": "Read", "description": "read a file", "input_schema": map[string]any{"type": "object"}},
		},
	}
}

func runClaudeWebTools(t *testing.T, upstream *webToolsUpstream, request *dto.ClaudeRequest, backend webtools.Backend) (*httptest.ResponseRecorder, *relaycommon.RelayInfo, *dto.Usage) {
	recorder, c := newWebToolsContext("/v1/messages", constant.ChannelTypeAnthropic, upstream.server.URL)
	info := relaycommon.GenRelayInfoClaude(c, request)
	info.InitChannelMeta(c)
	adaptor := GetAdaptor(info.ApiType)
	require.NotNil(t, adaptor)
	adaptor.Init(info)

	tools := claudeHostedWebTools(request, backend)
	require.Contains(t, tools, "web_search")
	usage, apiErr := relayClaudeWithWebTools(c, info, adaptor, request, tools, backend)
	require.Nil(t, apiErr)
	return recorder, info, usage
}

// The gateway rewrite must not re-encode client tools: Claude follows the
// schema property order, and a decoded map would sort it.
func TestClaudeWebToolsKeepsClientToolSchemaPropertyOrder(t *testing.T) {
	upstream := newWebToolsUpstream(t, claudeWebToolsReply)
	backend := &fakeWebToolsBackend{serves: true}
	var request dto.ClaudeRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"model":"test-model","max_tokens":1024,"stream":false,`+
		`"messages":[{"role":"user","content":"When was Go released?"}],`+
		`"tools":[{"type":"web_search_20250305","name":"web_search"},`+
		`{"name":"Read","input_schema":{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}}}}]}`, &request))

	runClaudeWebTools(t, upstream, &request, backend)

	require.NotEmpty(t, upstream.bodies)
	assert.Contains(t, upstream.bodies[0], `"properties":{"path":{"type":"string"},"limit":{"type":"integer"}}`)
}

func TestClaudeWebToolsAnswersSearchAndReturnsOneMessage(t *testing.T) {
	upstream := newWebToolsUpstream(t, claudeWebToolsReply)
	backend := &fakeWebToolsBackend{serves: true, hits: []webtools.SearchHit{
		{Title: "Go", URL: "https://go.dev/", Summary: "Go is an open source language.", Date: "2009-11-10T00:00:00+00:00"},
	}}
	recorder, info, usage := runClaudeWebTools(t, upstream, newClaudeWebToolsRequest(false), backend)

	require.Len(t, upstream.requests, 2)
	tools := upstream.requests[0]["tools"].([]any)
	require.Len(t, tools, 2)
	searchTool := tools[0].(map[string]any)
	assert.Equal(t, "web_search", searchTool["name"])
	assert.NotContains(t, searchTool, "type", "the upstream receives a function tool")
	assert.Contains(t, searchTool, "input_schema")
	assert.Equal(t, []string{"go release"}, backend.searches)

	messages := upstream.requests[1]["messages"].([]any)
	require.Len(t, messages, 3)
	assistant := messages[1].(map[string]any)
	assert.Equal(t, "assistant", assistant["role"])
	call := assistant["content"].([]any)[1].(map[string]any)
	assert.Equal(t, "tool_use", call["type"])
	assert.Equal(t, "toolu_1", call["id"])
	result := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	assert.Equal(t, "tool_result", result["type"])
	assert.Equal(t, "toolu_1", result["tool_use_id"])
	assert.Equal(t, "Search results for \"go release\":\n\n[1] Go\nURL: https://go.dev/\nPublished: 2009-11-10T00:00:00+00:00\nGo is an open source language.", result["content"])

	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "msg_1", response["id"])
	assert.Equal(t, "end_turn", response["stop_reason"])
	content := response["content"].([]any)
	require.Len(t, content, 4)
	assert.Equal(t, "text", content[0].(map[string]any)["type"])
	serverCall := content[1].(map[string]any)
	assert.Equal(t, "server_tool_use", serverCall["type"])
	assert.Equal(t, "srvtoolu_toolu_1", serverCall["id"])
	searchResult := content[2].(map[string]any)
	assert.Equal(t, "web_search_tool_result", searchResult["type"])
	assert.Equal(t, "srvtoolu_toolu_1", searchResult["tool_use_id"])
	hit := searchResult["content"].([]any)[0].(map[string]any)
	assert.Equal(t, "https://go.dev/", hit["url"])
	assert.Equal(t, "2009-11-10", hit["page_age"])
	assert.Equal(t, "Go was released.", content[3].(map[string]any)["text"])
	clientUsage := response["usage"].(map[string]any)
	assert.EqualValues(t, 40, clientUsage["input_tokens"])
	assert.EqualValues(t, 17, clientUsage["output_tokens"])
	assert.EqualValues(t, 1, clientUsage["server_tool_use"].(map[string]any)["web_search_requests"])

	// Settlement covers both rounds and the search at its tool price.
	assert.Equal(t, 40, usage.PromptTokens)
	assert.Equal(t, 17, usage.CompletionTokens)
	assert.Nil(t, usage.BillingUsage)
	require.NotNil(t, info.ResponsesUsageInfo)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[webtools.ToolWebSearch].CallCount)
	assert.False(t, info.IsStream)

	// A follow-up request replays the gateway round exactly as the upstream saw it.
	followUp := newClaudeWebToolsRequest(false)
	followUp.Messages = append(followUp.Messages,
		dto.ClaudeMessage{Role: "assistant", Content: content},
		dto.ClaudeMessage{Role: "user", Content: "Thanks"},
	)
	replayed, err := gatewayClaudeRequest(followUp, claudeHostedWebTools(followUp, backend))
	require.NoError(t, err)
	require.Len(t, replayed.Messages, 5)
	replayedPrefix, err := common.Marshal(replayed.Messages[:3])
	require.NoError(t, err)
	roundPrefix, err := common.Marshal(messages)
	require.NoError(t, err)
	assert.JSONEq(t, string(roundPrefix), string(replayedPrefix))
	assert.Equal(t, "assistant", replayed.Messages[3].Role)
	assert.Equal(t, "Thanks", replayed.Messages[4].GetStringContent())
}

func TestClaudeWebToolsStreamsRoundsAsOneMessage(t *testing.T) {
	useStreamingTimeout(t)
	upstream := newWebToolsUpstream(t, claudeWebToolsReply)
	backend := &fakeWebToolsBackend{serves: true, hits: []webtools.SearchHit{{Title: "Go", URL: "https://go.dev/"}}}
	recorder, info, usage := runClaudeWebTools(t, upstream, newClaudeWebToolsRequest(true), backend)

	events, _ := readSSE(t, recorder.Body.String())
	var names []string
	starts := map[float64]string{}
	for _, event := range events {
		names = append(names, event.name)
		if event.name == "content_block_start" {
			block := event.data["content_block"].(map[string]any)
			starts[event.data["index"].(float64)] = block["type"].(string)
		}
	}
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_start\n"))
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_delta\n"))
	assert.Equal(t, "message_stop", names[len(names)-1])
	assert.Equal(t, map[float64]string{0: "text", 1: "server_tool_use", 2: "web_search_tool_result", 3: "text"}, starts)

	delta := events[len(events)-2].data
	assert.Equal(t, "end_turn", delta["delta"].(map[string]any)["stop_reason"])
	assert.EqualValues(t, 40, delta["usage"].(map[string]any)["input_tokens"])
	assert.EqualValues(t, 17, delta["usage"].(map[string]any)["output_tokens"])

	assert.Equal(t, 40, usage.PromptTokens)
	assert.Equal(t, 17, usage.CompletionTokens)
	assert.True(t, info.IsStream)
	require.Len(t, upstream.requests, 2)
	assert.Equal(t, true, upstream.requests[1]["stream"])
}

func TestClaudeHostedWebToolsKeepsNativeToolsWithoutBackend(t *testing.T) {
	request := newClaudeWebToolsRequest(false)
	assert.Nil(t, claudeHostedWebTools(request, &fakeWebToolsBackend{serves: false}))

	tools := claudeHostedWebTools(request, &fakeWebToolsBackend{serves: true})
	require.Len(t, tools, 1)
	assert.Equal(t, 5, tools["web_search"].maxUses)
}

// responsesWebToolsReply serves a model that searches once and then answers.
func responsesWebToolsReply(w http.ResponseWriter, round int, stream bool) {
	if round == 1 {
		call := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"web_search","arguments":"{\"query\":\"go release\"}","status":"completed"}`
		response := `{"id":"resp_1","object":"response","created_at":1700000000,"status":"completed","model":"test-model","output":[` + call + `],"usage":{"input_tokens":10,"output_tokens":12,"total_tokens":22}}`
		if stream {
			writeSSE(w,
				`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1700000000,"status":"in_progress","model":"test-model","output":[]}}`,
				`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"web_search","arguments":"","status":"in_progress"}}`,
				`{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"query\":\"go release\"}"}`,
				`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":`+call+`}`,
				`{"type":"response.completed","sequence_number":4,"response":`+response+`}`,
			)
			return
		}
		writeJSON(w, response)
		return
	}
	message := `{"type":"message","id":"msg_2","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Go was released.","annotations":[]}]}`
	response := `{"id":"resp_2","object":"response","created_at":1700000001,"status":"completed","model":"test-model","output":[` + message + `],"usage":{"input_tokens":30,"output_tokens":5,"total_tokens":35}}`
	if stream {
		writeSSE(w,
			`{"type":"response.created","sequence_number":0,"response":{"id":"resp_2","object":"response","created_at":1700000001,"status":"in_progress","model":"test-model","output":[]}}`,
			`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_2","status":"in_progress","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","sequence_number":2,"output_index":0,"item_id":"msg_2","content_index":0,"delta":"Go was released."}`,
			`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":`+message+`}`,
			`{"type":"response.completed","sequence_number":4,"response":`+response+`}`,
		)
		return
	}
	writeJSON(w, response)
}

func newResponsesWebToolsRequest(stream bool) *dto.OpenAIResponsesRequest {
	return &dto.OpenAIResponsesRequest{
		Model:      "test-model",
		Stream:     &stream,
		Input:      json.RawMessage(`[{"type":"message","role":"user","content":"When was Go released?"}]`),
		Tools:      json.RawMessage(`[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"web_search","external_web_access":true}]`),
		ToolChoice: json.RawMessage(`{"type":"web_search"}`),
		Include:    json.RawMessage(`["reasoning.encrypted_content","web_search_call.action.sources"]`),
		Store:      json.RawMessage(`false`),
	}
}

func runResponsesWebTools(t *testing.T, upstream *webToolsUpstream, request *dto.OpenAIResponsesRequest, backend webtools.Backend) (*httptest.ResponseRecorder, *relaycommon.RelayInfo, *dto.Usage) {
	recorder, c := newWebToolsContext("/v1/responses", constant.ChannelTypeOpenAI, upstream.server.URL)
	info := relaycommon.GenRelayInfoResponses(c, request)
	info.InitChannelMeta(c)

	tools := responsesHostedWebTools(request, backend)
	require.Contains(t, tools, "web_search")
	usage, apiErr := relayResponsesWithWebTools(c, info, request, tools, backend)
	require.Nil(t, apiErr)
	return recorder, info, usage
}

func TestResponsesWebToolsAnswersSearchAndReturnsOneResponse(t *testing.T) {
	upstream := newWebToolsUpstream(t, responsesWebToolsReply)
	backend := &fakeWebToolsBackend{serves: true, hits: []webtools.SearchHit{{Title: "Go", URL: "https://go.dev/", Summary: "Go is an open source language."}}}
	recorder, info, usage := runResponsesWebTools(t, upstream, newResponsesWebToolsRequest(false), backend)

	require.Len(t, upstream.requests, 2)
	first := upstream.requests[0]
	tools := first["tools"].([]any)
	require.Len(t, tools, 2)
	assert.Equal(t, "shell", tools[0].(map[string]any)["name"])
	searchTool := tools[1].(map[string]any)
	assert.Equal(t, "function", searchTool["type"])
	assert.Equal(t, "web_search", searchTool["name"])
	assert.Equal(t, map[string]any{"type": "function", "name": "web_search"}, first["tool_choice"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, first["include"])
	assert.Equal(t, []string{"go release"}, backend.searches)

	second := upstream.requests[1]
	assert.Equal(t, "auto", second["tool_choice"], "a forced search is made once")
	input := second["input"].([]any)
	require.Len(t, input, 3)
	call := input[1].(map[string]any)
	assert.Equal(t, "function_call", call["type"])
	assert.Equal(t, "call_1", call["call_id"])
	assert.NotContains(t, call, "id", "items go back inline, without IDs to resolve")
	output := input[2].(map[string]any)
	assert.Equal(t, "function_call_output", output["type"])
	assert.Equal(t, "call_1", output["call_id"])
	assert.Equal(t, "Search results for \"go release\":\n\n[1] Go\nURL: https://go.dev/\nGo is an open source language.", output["output"])

	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "resp_1", response["id"])
	assert.Equal(t, "completed", response["status"])
	items := response["output"].([]any)
	require.Len(t, items, 2)
	searchCall := items[0].(map[string]any)
	assert.Equal(t, "web_search_call", searchCall["type"])
	assert.Equal(t, "completed", searchCall["status"])
	assert.Equal(t, map[string]any{
		"type":    "search",
		"query":   "go release",
		"sources": []any{map[string]any{"type": "url", "url": "https://go.dev/"}},
	}, searchCall["action"])
	assert.Equal(t, "message", items[1].(map[string]any)["type"])
	clientUsage := response["usage"].(map[string]any)
	assert.EqualValues(t, 40, clientUsage["input_tokens"])
	assert.EqualValues(t, 17, clientUsage["output_tokens"])

	assert.Equal(t, 40, usage.PromptTokens)
	assert.Equal(t, 17, usage.CompletionTokens)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[webtools.ToolWebSearch].CallCount)

	// A follow-up request replays the search as a call whose results are gone.
	followUp := newResponsesWebToolsRequest(false)
	history := append([]any{map[string]any{"type": "message", "role": "user", "content": "When was Go released?"}}, items...)
	history = append(history, map[string]any{"type": "message", "role": "user", "content": "Thanks"})
	var err error
	followUp.Input, err = common.Marshal(history)
	require.NoError(t, err)
	replayed, err := gatewayResponsesRequest(followUp, responsesHostedWebTools(followUp, backend))
	require.NoError(t, err)
	var replayedInput []map[string]any
	require.NoError(t, common.Unmarshal(replayed.Input, &replayedInput))
	require.Len(t, replayedInput, 5)
	assert.Equal(t, map[string]any{"type": "function_call", "call_id": "call_web_1", "name": "web_search", "arguments": `{"query":"go release"}`}, replayedInput[1])
	assert.Equal(t, "Search results for \"go release\" were returned in an earlier turn and are no longer available. Search again if they are needed.\n\nSources:\n- https://go.dev/", replayedInput[2]["output"])
	assert.Equal(t, "message", replayedInput[3]["type"])
}

func TestResponsesWebToolsStreamsRoundsAsOneResponse(t *testing.T) {
	useStreamingTimeout(t)
	upstream := newWebToolsUpstream(t, responsesWebToolsReply)
	backend := &fakeWebToolsBackend{serves: true, hits: []webtools.SearchHit{{Title: "Go", URL: "https://go.dev/"}}}
	recorder, info, usage := runResponsesWebTools(t, upstream, newResponsesWebToolsRequest(true), backend)

	events, _ := readSSE(t, recorder.Body.String())
	var names []string
	for i, event := range events {
		names = append(names, event.name)
		assert.Equal(t, event.name, event.data["type"])
		assert.EqualValues(t, i, event.data["sequence_number"])
	}
	assert.Equal(t, []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.web_search_call.in_progress", "response.web_search_call.searching",
		"response.web_search_call.completed", "response.output_item.done",
		"response.output_item.added", "response.output_text.delta", "response.output_item.done",
		"response.completed",
	}, names)
	assert.EqualValues(t, 0, events[2].data["output_index"])
	assert.EqualValues(t, 1, events[7].data["output_index"])
	assert.EqualValues(t, 1, events[8].data["output_index"])

	completed := events[len(events)-1].data["response"].(map[string]any)
	assert.Equal(t, "resp_1", completed["id"])
	output := completed["output"].([]any)
	require.Len(t, output, 2)
	assert.Equal(t, "web_search_call", output[0].(map[string]any)["type"])
	assert.Equal(t, "message", output[1].(map[string]any)["type"])
	assert.EqualValues(t, 40, completed["usage"].(map[string]any)["input_tokens"])

	assert.Equal(t, 40, usage.PromptTokens)
	assert.Equal(t, 17, usage.CompletionTokens)
	assert.True(t, info.IsStream)
	require.Len(t, upstream.requests, 2)
	assert.Equal(t, true, upstream.requests[1]["stream"])
}

// chatWebToolsReply serves a model that searches once and then answers.
func chatWebToolsReply(w http.ResponseWriter, round int, stream bool) {
	if round == 1 {
		if stream {
			writeChatSSE(w,
				`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
				`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":""}}]},"finish_reason":null}]}`,
				`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":\"go release\"}"}}]},"finish_reason":null}]}`,
				`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":12,"total_tokens":22}}`,
			)
			return
		}
		writeJSON(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"go release\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":12,"total_tokens":22}}`)
		return
	}
	if stream {
		writeChatSSE(w,
			`{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1700000001,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Go was "},"finish_reason":null}]}`,
			`{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1700000001,"model":"test-model","choices":[{"index":0,"delta":{"content":"released."},"finish_reason":null}]}`,
			`{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1700000001,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1700000001,"model":"test-model","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":5,"total_tokens":35}}`,
		)
		return
	}
	writeJSON(w, `{"id":"chatcmpl-2","object":"chat.completion","created":1700000001,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"Go was released."},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5,"total_tokens":35}}`)
}

func newChatWebToolsRequest(stream bool) *dto.GeneralOpenAIRequest {
	return &dto.GeneralOpenAIRequest{
		Model:            "test-model",
		Stream:           &stream,
		Messages:         []dto.Message{{Role: "user", Content: "When was Go released?"}},
		WebSearchOptions: &dto.WebSearchOptions{SearchContextSize: "medium"},
	}
}

func runChatWebTools(t *testing.T, upstream *webToolsUpstream, request *dto.GeneralOpenAIRequest, backend webtools.Backend) (*httptest.ResponseRecorder, *relaycommon.RelayInfo, *dto.Usage) {
	recorder, c := newWebToolsContext("/v1/chat/completions", constant.ChannelTypeOpenAI, upstream.server.URL)
	info := relaycommon.GenRelayInfoOpenAI(c, request)
	info.InitChannelMeta(c)
	info.ShouldIncludeUsage = true
	adaptor := GetAdaptor(info.ApiType)
	require.NotNil(t, adaptor)
	adaptor.Init(info)

	tools := chatHostedWebTools(request, backend)
	require.Contains(t, tools, "web_search")
	usage, apiErr := relayChatWithWebTools(c, info, adaptor, request, tools, backend)
	require.Nil(t, apiErr)
	return recorder, info, usage
}

func TestChatWebToolsAnswersSearchAndReturnsOneCompletion(t *testing.T) {
	upstream := newWebToolsUpstream(t, chatWebToolsReply)
	backend := &fakeWebToolsBackend{serves: true, hits: []webtools.SearchHit{{Title: "Go", URL: "https://go.dev/"}}}
	recorder, info, usage := runChatWebTools(t, upstream, newChatWebToolsRequest(false), backend)

	require.Len(t, upstream.requests, 2)
	first := upstream.requests[0]
	assert.NotContains(t, first, "web_search_options")
	tools := first["tools"].([]any)
	require.Len(t, tools, 1)
	assert.Equal(t, "web_search", tools[0].(map[string]any)["function"].(map[string]any)["name"])
	assert.Equal(t, []string{"go release"}, backend.searches)

	messages := upstream.requests[1]["messages"].([]any)
	require.Len(t, messages, 3)
	assistant := messages[1].(map[string]any)
	assert.Equal(t, "assistant", assistant["role"])
	assert.Equal(t, "call_1", assistant["tool_calls"].([]any)[0].(map[string]any)["id"])
	result := messages[2].(map[string]any)
	assert.Equal(t, "tool", result["role"])
	assert.Equal(t, "call_1", result["tool_call_id"])
	assert.Equal(t, "Search results for \"go release\":\n\n[1] Go\nURL: https://go.dev/", result["content"])

	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "chatcmpl-1", response["id"])
	choice := response["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "stop", choice["finish_reason"])
	message := choice["message"].(map[string]any)
	assert.Equal(t, "Go was released.", message["content"])
	assert.NotContains(t, message, "tool_calls")
	clientUsage := response["usage"].(map[string]any)
	assert.EqualValues(t, 40, clientUsage["prompt_tokens"])
	assert.EqualValues(t, 17, clientUsage["completion_tokens"])

	assert.Equal(t, 40, usage.PromptTokens)
	assert.Equal(t, 17, usage.CompletionTokens)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[webtools.ToolWebSearch].CallCount)
}

func TestChatWebToolsStreamsOnlyTheAnswer(t *testing.T) {
	useStreamingTimeout(t)
	upstream := newWebToolsUpstream(t, chatWebToolsReply)
	backend := &fakeWebToolsBackend{serves: true, hits: []webtools.SearchHit{{Title: "Go", URL: "https://go.dev/"}}}
	recorder, _, usage := runChatWebTools(t, upstream, newChatWebToolsRequest(true), backend)

	events, done := readSSE(t, recorder.Body.String())
	assert.Equal(t, 1, done)
	var text strings.Builder
	var finishReasons []any
	var usages []any
	for _, event := range events {
		assert.Equal(t, "chatcmpl-1", event.data["id"])
		choices := event.data["choices"].([]any)
		if len(choices) == 0 {
			usages = append(usages, event.data["usage"])
			continue
		}
		choice := choices[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		assert.NotContains(t, delta, "tool_calls")
		text.WriteString(common.Interface2String(delta["content"]))
		if reason := choice["finish_reason"]; reason != nil {
			finishReasons = append(finishReasons, reason)
		}
	}
	assert.Equal(t, "Go was released.", text.String())
	assert.Equal(t, []any{"stop"}, finishReasons)
	require.Len(t, usages, 1)
	assert.EqualValues(t, 40, usages[0].(map[string]any)["prompt_tokens"])

	assert.Equal(t, 40, usage.PromptTokens)
	assert.Equal(t, 17, usage.CompletionTokens)
	require.Len(t, upstream.requests, 2)
}

func TestChatWebToolsLeavesGatewayCallsBesideClientCallsUnrun(t *testing.T) {
	upstream := newWebToolsUpstream(t, func(w http.ResponseWriter, _ int, _ bool) {
		writeJSON(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"Checking.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"go\"}"}},{"id":"call_2","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":12,"total_tokens":22}}`)
	})
	backend := &fakeWebToolsBackend{serves: true}
	request := newChatWebToolsRequest(false)
	request.Tools = []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	recorder, info, _ := runChatWebTools(t, upstream, request, backend)

	assert.Len(t, upstream.requests, 1)
	assert.Empty(t, backend.searches)
	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	choice := response["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "tool_calls", choice["finish_reason"])
	message := choice["message"].(map[string]any)
	assert.Equal(t, "Checking.", message["content"])
	calls := message["tool_calls"].([]any)
	require.Len(t, calls, 1)
	assert.Equal(t, "call_2", calls[0].(map[string]any)["id"])
	if info.ResponsesUsageInfo != nil {
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, webtools.ToolWebSearch)
	}
}
