package vercel

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiUpstreamModelDetection(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"google/gemini-3.8-flash", true},
		{"gemini-3.8-flash", true},
		{"Google/Gemini-2.5-Pro", true},
		{"google/gemma-3-27b-it", true},
		{"anthropic/claude-sonnet-4", false},
		{"claude-sonnet-4-20250514", false},
		{"openai/gpt-5.5", false},
		{"", false},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.want, geminiUpstream(tc.model, nil), "model=%q", tc.model)
	}

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "google/gemini-3.8-flash"}}
	assert.True(t, geminiUpstream("", info), "falls back to info.UpstreamModelName")
}

func TestSanitizeClaudeThinkingSignatures(t *testing.T) {
	t.Run("keeps thinking text and drops signature", func(t *testing.T) {
		content := []any{
			map[string]any{"type": "thinking", "thinking": "Run ls.", "signature": "sig123"},
			map[string]any{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": map[string]any{"command": "ls"}},
		}
		got := sanitizeClaudeThinkingSignatures(content)
		blocks, ok := got.([]any)
		require.True(t, ok)
		require.Len(t, blocks, 2)

		thinking, ok := blocks[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "thinking", thinking["type"])
		assert.Equal(t, "Run ls.", thinking["thinking"])
		_, hasSig := thinking["signature"]
		assert.False(t, hasSig, "signature must be removed (H8-H10 400)")

		toolUse, ok := blocks[1].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "tool_use", toolUse["type"])
		assert.Equal(t, "toolu_01", toolUse["id"], "tool_use must stay intact")
	})

	t.Run("drops redacted_thinking and keeps unknown blocks", func(t *testing.T) {
		content := []any{
			map[string]any{"type": "redacted_thinking", "data": "opaque"},
			map[string]any{"type": "text", "text": "hi", "cache_control": map[string]any{"type": "ephemeral"}},
			"not-a-map",
		}
		got := sanitizeClaudeThinkingSignatures(content)
		blocks, ok := got.([]any)
		require.True(t, ok)
		require.Len(t, blocks, 2)
		assert.Equal(t, "text", blocks[0].(map[string]any)["type"])
		assert.NotNil(t, blocks[0].(map[string]any)["cache_control"], "unknown fields must be preserved")
		assert.Equal(t, "not-a-map", blocks[1])
	})

	t.Run("string content is untouched", func(t *testing.T) {
		assert.Equal(t, "plain", sanitizeClaudeThinkingSignatures("plain"))
		assert.Nil(t, sanitizeClaudeThinkingSignatures(nil))
	})
}

func TestHoistOpenAISystemMessages(t *testing.T) {
	t.Run("mid-array system and developer move to front", func(t *testing.T) {
		messages := []dto.Message{
			{Role: "system", Content: "Main prompt"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "system", Content: "Jailbreak"},
			{Role: "developer", Content: "Dev note"},
			{Role: "user", Content: "Say OK only."},
		}
		got := hoistOpenAISystemMessages(messages)
		require.Len(t, got, 6)
		assert.Equal(t, "system", got[0].Role)
		assert.Equal(t, "Main prompt", got[0].Content)
		assert.Equal(t, "system", got[1].Role)
		assert.Equal(t, "Jailbreak", got[1].Content)
		assert.Equal(t, "developer", got[2].Role)
		assert.Equal(t, "user", got[3].Role)
		assert.Equal(t, "hi", got[3].Content)
		assert.Equal(t, "assistant", got[4].Role)
		assert.Equal(t, "user", got[5].Role)
		assert.Equal(t, "Say OK only.", got[5].Content)
	})

	t.Run("no system messages returns input unchanged", func(t *testing.T) {
		messages := []dto.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		}
		got := hoistOpenAISystemMessages(messages)
		assert.Equal(t, messages, got)
	})
}

func vercelInfo(upstreamModel string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: upstreamModel,
		},
	}
}

func TestConvertClaudeRequestStripsSignaturesForGeminiUpstream(t *testing.T) {
	adaptor := &Adaptor{}
	maxTokens := uint(300)
	request := &dto.ClaudeRequest{
		Model:     "google/gemini-3.8-flash",
		MaxTokens: &maxTokens,
		Messages: []dto.ClaudeMessage{
			{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "thinking", "thinking": "Run ls.", "signature": "sig123"},
					map[string]any{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": map[string]any{"command": "ls"}},
				},
			},
		},
	}

	_, err := adaptor.ConvertClaudeRequest(nil, vercelInfo("google/gemini-3.8-flash"), request)
	require.NoError(t, err)

	blocks, ok := request.Messages[0].Content.([]any)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	thinking := blocks[0].(map[string]any)
	assert.Equal(t, "Run ls.", thinking["thinking"])
	_, hasSig := thinking["signature"]
	assert.False(t, hasSig)
	assert.Equal(t, "tool_use", blocks[1].(map[string]any)["type"])
}

func TestConvertClaudeRequestKeepsSignaturesForAnthropicUpstream(t *testing.T) {
	adaptor := &Adaptor{}
	maxTokens := uint(300)
	request := &dto.ClaudeRequest{
		Model:     "anthropic/claude-sonnet-4",
		MaxTokens: &maxTokens,
		Messages: []dto.ClaudeMessage{
			{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "thinking", "thinking": "Run ls.", "signature": "sig123"},
				},
			},
		},
	}

	_, err := adaptor.ConvertClaudeRequest(nil, vercelInfo("anthropic/claude-sonnet-4"), request)
	require.NoError(t, err)

	blocks, ok := request.Messages[0].Content.([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	assert.Equal(t, "sig123", blocks[0].(map[string]any)["signature"],
		"anthropic/* replay needs valid thinking signatures")
}

func TestConvertOpenAIRequestHoistsSystemForGeminiUpstream(t *testing.T) {
	adaptor := &Adaptor{}
	request := &dto.GeneralOpenAIRequest{
		Model: "google/gemini-3.8-flash",
		Messages: []dto.Message{
			{Role: "system", Content: "Main prompt"},
			{Role: "user", Content: "hi"},
			{Role: "system", Content: "Jailbreak"},
			{Role: "user", Content: "Say OK only."},
		},
	}

	_, err := adaptor.ConvertOpenAIRequest(nil, vercelInfo("google/gemini-3.8-flash"), request)
	require.NoError(t, err)

	require.Len(t, request.Messages, 4)
	assert.Equal(t, "system", request.Messages[0].Role)
	assert.Equal(t, "Main prompt", request.Messages[0].Content)
	assert.Equal(t, "system", request.Messages[1].Role)
	assert.Equal(t, "Jailbreak", request.Messages[1].Content)
	assert.Equal(t, "user", request.Messages[2].Role)
}

func TestConvertOpenAIRequestKeepsOrderForOpenAIUpstream(t *testing.T) {
	adaptor := &Adaptor{}
	request := &dto.GeneralOpenAIRequest{
		Model: "openai/gpt-5.5",
		Messages: []dto.Message{
			{Role: "system", Content: "Main prompt"},
			{Role: "user", Content: "hi"},
			{Role: "system", Content: "Jailbreak"},
		},
	}

	_, err := adaptor.ConvertOpenAIRequest(nil, vercelInfo("openai/gpt-5.5"), request)
	require.NoError(t, err)

	require.Len(t, request.Messages, 3)
	assert.Equal(t, "system", request.Messages[0].Role)
	assert.Equal(t, "user", request.Messages[1].Role, "non-Gemini upstream must keep message order")
	assert.Equal(t, "system", request.Messages[2].Role)
}

func TestAdaptorIdentity(t *testing.T) {
	adaptor := &Adaptor{}
	assert.Equal(t, "vercel", adaptor.GetChannelName())
	assert.Empty(t, adaptor.GetModelList())
}

func TestVercelChannelRegistration(t *testing.T) {
	assert.Equal(t, 64, constant.ChannelTypeVercel)
	assert.Equal(t, "Vercel", constant.GetChannelTypeName(constant.ChannelTypeVercel))
	assert.Equal(t, "https://ai-gateway.vercel.sh", constant.GetChannelBaseURL(constant.ChannelTypeVercel))
	assert.Equal(t, "vLLM", constant.GetChannelTypeName(constant.ChannelTypeVLLM))
	assert.Equal(t, "SGLang", constant.GetChannelTypeName(constant.ChannelTypeSGLang))

	apiType, ok := common.ChannelType2APIType(constant.ChannelTypeVercel)
	require.True(t, ok)
	assert.Equal(t, constant.APITypeVercel, apiType)
	assert.False(t, common.SupportsResponsesCompact(constant.ChannelTypeVercel, apiType))
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
		constant.EndpointTypeAnthropic,
		constant.EndpointTypeGemini,
	}, common.GetEndpointTypesByChannelType(constant.ChannelTypeVercel, "google/gemini-3.8-flash"))
}
