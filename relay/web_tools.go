package relay

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/webtools"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// Gateway web tools.
//
// When the gateway runs the hosted web tools of a request, the upstream
// receives function tools in their place. Every upstream round is relayed by
// the normal path of the request's protocol into a writer that collects the
// round as the client would have received it; the gateway answers the
// function calls and sends the next round until the model stops calling
// them. The client receives one response covering all rounds. Each protocol
// presents the gateway calls in its own way (claude_web_tools.go,
// responses_web_tools.go, chat_web_tools.go).

const webToolsWriteTimeout = 30 * time.Second

// hostedWebTool is a web search or fetch tool the gateway runs for a request,
// under the function name the upstream calls.
type hostedWebTool struct {
	kind             string
	name             string
	maxUses          int
	allowedDomains   []string
	blockedDomains   []string
	maxContentTokens int
}

// gatewayWebTools returns the functions standing in for a hosted web search
// tool that has no name of its own: a search, and a fetch when a channel
// serves it, each under a name no client tool of the request uses. It returns
// nil when no search can be offered.
func gatewayWebTools(taken map[string]bool, backend webtools.Backend, allowedDomains []string) map[string]*hostedWebTool {
	tools := make(map[string]*hostedWebTool)
	for _, kind := range []string{webtools.ToolWebSearch, webtools.ToolWebFetch} {
		if !backend.Serves(kind) {
			continue
		}
		for _, name := range []string{kind, "newapi_" + kind} {
			if !taken[name] {
				tools[name] = &hostedWebTool{kind: kind, name: name, allowedDomains: allowedDomains}
				break
			}
		}
	}
	if webToolOfKind(tools, webtools.ToolWebSearch) == nil {
		return nil
	}
	return tools
}

func webToolOfKind(tools map[string]*hostedWebTool, kind string) *hostedWebTool {
	for _, tool := range tools {
		if tool.kind == kind {
			return tool
		}
	}
	return nil
}

// orderedWebTools lists the tools search first, so a rewritten request does
// not depend on map order.
func orderedWebTools(tools map[string]*hostedWebTool) []*hostedWebTool {
	ordered := make([]*hostedWebTool, 0, len(tools))
	for _, kind := range []string{webtools.ToolWebSearch, webtools.ToolWebFetch} {
		for _, tool := range tools {
			if tool.kind == kind {
				ordered = append(ordered, tool)
			}
		}
	}
	return ordered
}

// webToolFunction returns the description and parameter schema of the
// function the upstream calls for a tool kind.
func webToolFunction(kind string) (string, map[string]any) {
	if kind == webtools.ToolWebFetch {
		return "Fetch a web page by URL and return its readable content as Markdown. Use it to read a page found by a search or given by the user.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "The http or https URL of the page"},
				},
				"required": []any{"url"},
			}
	}
	return "Search the web for current information. Returns result titles, URLs, publication dates and summaries.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "The search query"},
			},
			"required": []any{"query"},
		}
}

func webToolStrings(value any) []string {
	items, _ := value.([]any)
	values := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(common.Interface2String(item)); text != "" {
			values = append(values, text)
		}
	}
	return values
}

// webToolArguments decodes the arguments of a function call, a JSON string
// in Chat Completions and Responses.
func webToolArguments(arguments any) map[string]any {
	switch arguments := arguments.(type) {
	case map[string]any:
		return arguments
	case string:
		var decoded map[string]any
		if err := common.UnmarshalJsonStr(arguments, &decoded); err == nil && decoded != nil {
			return decoded
		}
	}
	return map[string]any{}
}

// webToolResult is the outcome of one gateway call.
type webToolResult struct {
	kind      string
	query     string
	url       string
	hits      []webtools.SearchHit
	title     string
	content   string
	errorCode string
}

// text is the function result the upstream model reads.
func (r *webToolResult) text() string {
	if r.errorCode != "" {
		return "Error: " + r.errorCode
	}
	if r.kind == webtools.ToolWebFetch {
		return fmt.Sprintf("Title: %s\nURL: %s\n\n%s", r.title, r.url, r.content)
	}
	if len(r.hits) == 0 {
		return fmt.Sprintf("No results found for %q.", r.query)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Search results for %q:", r.query)
	for i, hit := range r.hits {
		fmt.Fprintf(&text, "\n\n[%d] %s\nURL: %s", i+1, hit.Title, hit.URL)
		if hit.Date != "" {
			fmt.Fprintf(&text, "\nPublished: %s", hit.Date)
		}
		if hit.Summary != "" {
			text.WriteByte('\n')
			text.WriteString(hit.Summary)
		}
	}
	return text.String()
}

// webToolsRun answers the gateway calls of one request.
type webToolsRun struct {
	backend   webtools.Backend
	setting   *operation_setting.WebToolsSetting
	callLimit int
	uses      map[string]int
	calls     int
	billable  map[string]int
}

func newWebToolsRun(backend webtools.Backend, setting *operation_setting.WebToolsSetting) *webToolsRun {
	return &webToolsRun{
		backend:   backend,
		setting:   setting,
		callLimit: setting.CallLimit(),
		uses:      make(map[string]int),
		billable:  make(map[string]int),
	}
}

// call runs one function call of a gateway tool.
func (run *webToolsRun) call(c *gin.Context, tool *hostedWebTool, arguments map[string]any) *webToolResult {
	result := &webToolResult{kind: tool.kind}
	if tool.kind == webtools.ToolWebFetch {
		result.url = strings.TrimSpace(common.Interface2String(arguments["url"]))
	} else {
		result.query = common.Interface2String(arguments["query"])
	}
	if (tool.maxUses > 0 && run.uses[tool.name] >= tool.maxUses) || run.calls >= run.callLimit {
		result.errorCode = webtools.ErrorMaxUsesExceeded
		return result
	}
	run.uses[tool.name]++
	run.calls++
	if tool.kind == webtools.ToolWebFetch {
		run.fetch(c, tool, result)
	} else {
		run.search(c, tool, result)
	}
	return result
}

func (run *webToolsRun) search(c *gin.Context, tool *hostedWebTool, result *webToolResult) {
	query := strings.TrimSpace(result.query)
	if query == "" {
		result.errorCode = webtools.ErrorInvalidInput
		return
	}
	hits, err := run.backend.Search(c, webtools.SearchQuery(query, tool.allowedDomains, tool.blockedDomains), run.setting.ResultCount())
	if err != nil {
		result.errorCode = webToolErrorCode(err)
		return
	}
	// DeepSeek bills a search that returns results, whatever the filter keeps.
	if len(hits) > 0 {
		run.billable[tool.kind]++
	}
	result.hits = webtools.FilterHits(hits, tool.allowedDomains, tool.blockedDomains)
}

func (run *webToolsRun) fetch(c *gin.Context, tool *hostedWebTool, result *webToolResult) {
	if err := webtools.CheckURL(result.url, tool.allowedDomains, tool.blockedDomains); err != nil {
		result.errorCode = webToolErrorCode(err)
		return
	}
	page, err := run.backend.Fetch(c, result.url)
	if err != nil {
		result.errorCode = webToolErrorCode(err)
		return
	}
	if page.Content == "" {
		result.errorCode = webtools.ErrorURLNotAccessible
		return
	}
	run.billable[tool.kind]++
	limit := run.setting.FetchCharLimit()
	if tool.maxContentTokens > 0 {
		// About four characters per token.
		limit = min(limit, tool.maxContentTokens*4)
	}
	result.title = page.Title
	result.content = page.Content
	if utf8.RuneCountInString(result.content) > limit {
		result.content = string([]rune(result.content)[:limit]) + "\n\n[Content truncated]"
	}
}

// bill records the billable calls under the web_search and web_fetch tool
// prices.
func (run *webToolsRun) bill(info *relaycommon.RelayInfo) {
	for _, kind := range []string{webtools.ToolWebSearch, webtools.ToolWebFetch} {
		if count := run.billable[kind]; count > 0 {
			info.SetBillableToolCount(kind, count)
		}
	}
}

func webToolErrorCode(err error) string {
	if toolErr, ok := errors.AsType[*webtools.ToolError](err); ok {
		return toolErr.Code
	}
	return webtools.ErrorUnavailable
}

// resetWebToolsRound returns the relay info to the state of the first round.
// The converters keep per-response state on it.
func resetWebToolsRound(info *relaycommon.RelayInfo, upstreamModelName string, reasoningEffort string) {
	info.ClaudeToChatStreamState = nil
	info.ChatToGeminiStreamState = nil
	info.SendResponseCount = 0
	info.ThinkingContentInfo = relaycommon.ThinkingContentInfo{}
	info.ClaudeConvertInfo = &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone}
	info.FinalRequestRelayFormat = ""
	info.RequestConversionChain = nil
	info.InitRequestConversionChain()
	info.UpstreamModelName, info.ReasoningEffort = upstreamModelName, reasoningEffort
}

// addWebToolsRoundUsage adds the settled usage of one round to the request
// total. A round is taken in its canonical form, so the total carries no
// provider snapshot that settlement would prefer over the sum.
func addWebToolsRoundUsage(total *dto.Usage, round *dto.Usage) {
	if round == nil {
		return
	}
	if canonical, ok := round.BillingUsage.CanonicalUsage(); ok {
		round = canonical
	}
	total.PromptTokens += round.PromptTokens
	total.CompletionTokens += round.CompletionTokens
	total.TotalTokens += round.TotalTokens
	total.PromptCacheHitTokens += round.PromptCacheHitTokens
	total.InputTokens += round.InputTokens
	total.OutputTokens += round.OutputTokens
	total.ClaudeCacheCreation5mTokens += round.ClaudeCacheCreation5mTokens
	total.ClaudeCacheCreation1hTokens += round.ClaudeCacheCreation1hTokens
	addInputTokenDetails(&total.PromptTokensDetails, round.PromptTokensDetails)
	if round.InputTokensDetails != nil {
		if total.InputTokensDetails == nil {
			total.InputTokensDetails = &dto.InputTokenDetails{}
		}
		addInputTokenDetails(total.InputTokensDetails, *round.InputTokensDetails)
	}
	total.CompletionTokenDetails.TextTokens += round.CompletionTokenDetails.TextTokens
	total.CompletionTokenDetails.AudioTokens += round.CompletionTokenDetails.AudioTokens
	total.CompletionTokenDetails.ImageTokens += round.CompletionTokenDetails.ImageTokens
	total.CompletionTokenDetails.ReasoningTokens += round.CompletionTokenDetails.ReasoningTokens
	if round.UsageSemantic != "" {
		total.UsageSemantic = round.UsageSemantic
	}
	if round.UsageSource != "" {
		total.UsageSource = round.UsageSource
	}
	if cost, ok := round.Cost.(float64); ok {
		previous, _ := total.Cost.(float64)
		total.Cost = previous + cost
	}
}

func addInputTokenDetails(total *dto.InputTokenDetails, round dto.InputTokenDetails) {
	total.CachedTokens += round.CachedTokens
	total.CachedCreationTokens += round.CachedCreationTokens
	total.CacheWriteTokens += round.CacheWriteTokens
	total.TextTokens += round.TextTokens
	total.AudioTokens += round.AudioTokens
	total.ImageTokens += round.ImageTokens
}

// addUsageFields adds the numbers of a client usage object to a total,
// nested objects such as input_tokens_details included.
func addUsageFields(total map[string]any, round map[string]any) {
	for key, value := range round {
		switch value := value.(type) {
		case float64:
			previous, _ := total[key].(float64)
			total[key] = previous + value
		case map[string]any:
			nested, ok := total[key].(map[string]any)
			if !ok {
				nested = make(map[string]any, len(value))
				total[key] = nested
			}
			addUsageFields(nested, value)
		default:
			if _, exists := total[key]; !exists {
				total[key] = value
			}
		}
	}
}

func jsonInt(value any) int {
	number, _ := value.(float64)
	return int(number)
}

// webToolsStream writes the response of a streaming client.
type webToolsStream struct {
	client      gin.ResponseWriter
	mu          sync.Mutex
	headersSent bool
	failed      bool
}

func (s *webToolsStream) write(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return
	}
	if !s.headersSent {
		header := s.client.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		s.headersSent = true
	}
	_ = http.NewResponseController(s.client).SetWriteDeadline(time.Now().Add(webToolsWriteTimeout))
	if _, err := s.client.Write([]byte(text)); err != nil {
		s.failed = true
		return
	}
	s.client.Flush()
}

// writeEvent writes a named event, as Messages and Responses streams do.
func (s *webToolsStream) writeEvent(eventType string, payload any) {
	data, err := common.Marshal(payload)
	if err != nil {
		return
	}
	s.write("event: " + eventType + "\ndata: " + string(data) + "\n\n")
}

// writeData writes an unnamed event, as Chat Completions streams do.
func (s *webToolsStream) writeData(payload any) {
	data, err := common.Marshal(payload)
	if err != nil {
		return
	}
	s.write("data: " + string(data) + "\n\n")
}

// webToolsRoundWriter takes the client-format output of one upstream round.
// For a streaming client each event reaches onEvent as it arrives; otherwise
// the body is parsed when the round ends.
type webToolsRoundWriter struct {
	stream    *webToolsStream
	live      bool
	onEvent   func(event map[string]any)
	header    http.Header
	status    int
	size      int
	mu        sync.Mutex
	body      bytes.Buffer
	pending   []byte
	eventData strings.Builder
}

func newWebToolsRoundWriter(stream *webToolsStream, live bool, onEvent func(event map[string]any)) *webToolsRoundWriter {
	return &webToolsRoundWriter{stream: stream, live: live, onEvent: onEvent, header: make(http.Header)}
}

func (w *webToolsRoundWriter) Header() http.Header { return w.header }

func (w *webToolsRoundWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *webToolsRoundWriter) WriteHeaderNow() { w.WriteHeader(http.StatusOK) }

func (w *webToolsRoundWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	w.size += len(data)
	if !w.live {
		return w.body.Write(data)
	}
	w.pending = append(w.pending, data...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		line := strings.TrimRight(string(w.pending[:end]), "\r")
		w.pending = w.pending[end+1:]
		w.line(line)
	}
	return len(data), nil
}

func (w *webToolsRoundWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

func (w *webToolsRoundWriter) line(line string) {
	switch {
	case line == "":
		w.dispatch()
	case strings.HasPrefix(line, ":"):
		// Keepalive comments keep the client connection open between rounds.
		if w.live {
			w.stream.write(": PING\n\n")
		}
	case strings.HasPrefix(line, "data:"):
		if w.eventData.Len() > 0 {
			w.eventData.WriteByte('\n')
		}
		w.eventData.WriteString(strings.TrimSpace(line[len("data:"):]))
	}
}

func (w *webToolsRoundWriter) dispatch() {
	if w.eventData.Len() == 0 {
		return
	}
	data := w.eventData.String()
	w.eventData.Reset()
	var event map[string]any
	// [DONE] and other non-object data end nothing; the round ends with the relay.
	if err := common.UnmarshalJsonStr(data, &event); err != nil || event == nil {
		return
	}
	w.onEvent(event)
}

// complete parses whatever the round left: a trailing event without its blank
// line, or the whole body of a non-live round. A JSON body is returned to the
// caller; the events of an event stream body go to onEvent.
func (w *webToolsRoundWriter) complete() (map[string]any, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live {
		if len(w.pending) > 0 {
			w.line(strings.TrimRight(string(w.pending), "\r"))
			w.pending = nil
		}
		w.dispatch()
		return nil, nil
	}
	body := bytes.TrimSpace(w.body.Bytes())
	if bytes.HasPrefix(body, []byte("{")) {
		var response map[string]any
		if err := common.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		return response, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), len(body)+1)
	for scanner.Scan() {
		w.line(strings.TrimRight(scanner.Text(), "\r"))
	}
	w.dispatch()
	return nil, scanner.Err()
}

func (w *webToolsRoundWriter) Status() int   { return w.status }
func (w *webToolsRoundWriter) Size() int     { return w.size }
func (w *webToolsRoundWriter) Written() bool { return w.status != 0 }
func (w *webToolsRoundWriter) Flush()        {}

func (w *webToolsRoundWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack is not supported while relaying a web tools round")
}

func (w *webToolsRoundWriter) CloseNotify() <-chan bool {
	return w.stream.client.CloseNotify()
}

func (w *webToolsRoundWriter) Pusher() http.Pusher { return nil }
