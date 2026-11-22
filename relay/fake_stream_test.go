package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelFakeStreamChatCompletions(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 10
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	const result = `{"id":"chatcmpl-test","object":"chat.completion","created":17,"model":"gemini-upstream","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"推理","content":"你好 🌙\n第二行","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"id\":17}"},"extra_content":{"google":{"thought_signature":"sig"}}}]},"finish_reason":"tool_calls"},{"index":1,"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":8,"completion_tokens":7,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2}}}`
	for _, tc := range []struct {
		name, reply                                              string
		settings                                                 string
		channelType, status                                      int
		stream, includeUsage, fakeStream, badBody, unexpectedSSE bool
	}{
		{name: "buffered request preserves tools, thinking, choices, stop reasons and usage", reply: result, settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeSub2API, stream: true, includeUsage: true, fakeStream: true},
		{name: "client can omit the usage frame without changing settlement", reply: result, settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeNewAPI, stream: true, fakeStream: true},
		{name: "invalid JSON cannot produce success frames", reply: `{"choices":[`, settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeSub2API, stream: true, fakeStream: true, badBody: true},
		{name: "missing finish reason cannot produce success frames", reply: `{"choices":[{"index":0,"message":{"content":"hi"}}]}`, settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeSub2API, stream: true, fakeStream: true, badBody: true, status: http.StatusBadGateway},
		{name: "upstream HTTP errors remain errors", reply: `{"error":{"type":"rate_limit","message":"too many requests"}}`, settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeSub2API, stream: true, fakeStream: true, badBody: true, status: http.StatusTooManyRequests},
		{name: "error envelope inside HTTP 200 does not become success", reply: `{"error":{"type":"api_error","message":"provider failed"}}`, settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeSub2API, stream: true, fakeStream: true, badBody: true, status: http.StatusBadGateway},
		{name: "upstream cannot silently switch back to SSE", settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeSub2API, stream: true, fakeStream: true, badBody: true, unexpectedSSE: true, status: http.StatusBadGateway},
		{name: "request body passthrough still streams upstream", settings: `{"fake_stream":true,"pass_through_body_enabled":true}`, channelType: constant.ChannelTypeSub2API, stream: true},
		{name: "ordinary non-streaming clients still receive JSON", reply: result, settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeSub2API},
		{name: "disabled channel still streams upstream", settings: `{}`, channelType: constant.ChannelTypeSub2API, stream: true},
		{name: "unsupported channel type still streams upstream", settings: `{"fake_stream":true}`, channelType: constant.ChannelTypeOpenAI, stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type capture struct{ body, accept string }
			got := make(chan capture, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "unreadable request", http.StatusBadRequest)
					return
				}
				got <- capture{string(body), r.Header.Get("Accept")}
				if (tc.stream && !tc.fakeStream) || tc.unexpectedSSE {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"real-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"original\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.status == http.StatusTooManyRequests {
					w.WriteHeader(tc.status)
				}
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer server.Close()

			var settings dto.ChannelSettings
			require.NoError(t, common.UnmarshalJsonStr(tc.settings, &settings))
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Request.Header.Set("Content-Type", "application/json")
			if tc.stream {
				c.Request.Header.Set("Accept", "text/event-stream")
			}
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channelType)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			common.SetContextKey(c, constant.ContextKeyChannelKey, "test-key")
			common.SetContextKey(c, constant.ContextKeyChannelSetting, settings)
			if tc.fakeStream {
				// Channel overrides must not silently re-enable upstream streaming.
				common.SetContextKey(c, constant.ContextKeyChannelParamOverride, map[string]any{"stream": true})
			}
			request := &dto.GeneralOpenAIRequest{Model: "gemini-upstream", Stream: &tc.stream}
			if tc.includeUsage {
				request.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
			}
			info := relaycommon.GenRelayInfoOpenAI(c, request)
			info.InitChannelMeta(c)
			info.ShouldIncludeUsage = tc.includeUsage
			adaptor := GetAdaptor(info.ApiType)
			require.NotNil(t, adaptor)
			adaptor.Init(info)
			usage, apiErr := relayTextRequest(c, info, adaptor, request)
			if !tc.badBody {
				require.Nil(t, apiErr)
			}
			require.Len(t, got, 1)
			upstream := <-got
			assert.Equal(t, tc.stream && !tc.fakeStream, ojson.Get(upstream.body, "stream").Bool())
			assert.Equal(t, tc.stream, info.IsStream)
			if tc.fakeStream {
				assert.Equal(t, "application/json", upstream.accept)
				assert.False(t, ojson.Get(upstream.body, "stream_options").Exists())
			}
			if tc.badBody {
				require.NotNil(t, apiErr)
				if tc.status != 0 {
					assert.Equal(t, tc.status, apiErr.StatusCode)
				}
				assert.Empty(t, w.Body.String())
				return
			}
			if !tc.stream {
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
				assert.JSONEq(t, result, w.Body.String())
				return
			}
			require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
			if !tc.fakeStream {
				assert.Contains(t, w.Body.String(), "real-stream")
				return
			}
			require.Equal(t, 15, usage.TotalTokens)
			assert.Equal(t, 2, usage.PromptTokensDetails.CachedTokens)
			frames := strings.Split(strings.TrimSpace(w.Body.String()), "\n\n")
			require.Equal(t, "data: [DONE]", frames[len(frames)-1])
			seen := map[string]string{}
			for _, frame := range frames[:len(frames)-1] {
				chunk := strings.TrimPrefix(frame, "data: ")
				require.Equal(t, "chat.completion.chunk", ojson.Get(chunk, "object").Str)
				if ojson.Get(chunk, "usage.total_tokens").Exists() {
					assert.Equal(t, int64(0), ojson.Get(chunk, "choices.#").Int())
					seen["usage"] = ojson.Get(chunk, "usage.total_tokens").String()
				}
				for _, choice := range ojson.Get(chunk, "choices").Array() {
					for _, path := range []string{"delta.content", "delta.reasoning_content", "delta.tool_calls.0.index", "delta.tool_calls.0.function.arguments", "delta.tool_calls.0.extra_content.google.thought_signature", "finish_reason"} {
						if val := choice.Get(path); val.Exists() && val.Type != ojson.Null {
							seen[choice.Get("index").String()+"/"+path] = val.String()
						}
					}
				}
			}
			assert.Equal(t, "assistant", ojson.Get(strings.TrimPrefix(frames[0], "data: "), "choices.0.delta.role").Str)
			assert.Equal(t, "你好 🌙\n第二行", seen["0/delta.content"])
			assert.Equal(t, "推理", seen["0/delta.reasoning_content"])
			assert.Equal(t, "0", seen["0/delta.tool_calls.0.index"])
			assert.Equal(t, `{"id":17}`, seen["0/delta.tool_calls.0.function.arguments"])
			assert.Equal(t, "sig", seen["0/delta.tool_calls.0.extra_content.google.thought_signature"])
			assert.Equal(t, "tool_calls", seen["0/finish_reason"])
			assert.Equal(t, "partial", seen["1/delta.content"])
			assert.Equal(t, "length", seen["1/finish_reason"])
			_, hasUsage := seen["usage"]
			assert.Equal(t, tc.includeUsage, hasUsage)
		})
	}
}

func TestChannelFakeStreamCancelsUpstreamWhileBuffering(t *testing.T) {
	testCtx, finish := context.WithTimeout(t.Context(), 5*time.Second)
	defer finish()
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	headersWritten := make(chan struct{})
	upstreamStopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.(http.Flusher).Flush()
		close(headersWritten)
		select {
		case <-r.Context().Done():
			close(upstreamStopped)
		case <-testCtx.Done():
		}
	}))
	defer server.Close()
	go func() {
		select {
		case <-headersWritten:
			cancel()
		case <-ctx.Done():
		}
	}()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeSub2API)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{FakeStream: true})
	request := &dto.GeneralOpenAIRequest{Model: "gemini-upstream", Stream: common.GetPointer(true)}
	info := relaycommon.GenRelayInfoOpenAI(c, request)
	info.InitChannelMeta(c)
	adaptor := GetAdaptor(info.ApiType)
	require.NotNil(t, adaptor)
	adaptor.Init(info)
	_, apiErr := relayTextRequest(c, info, adaptor, request)
	require.NotNil(t, apiErr)
	assert.Empty(t, w.Body.String())
	select {
	case <-upstreamStopped:
	case <-testCtx.Done():
		t.Fatal("upstream did not observe client cancellation")
	}
}
