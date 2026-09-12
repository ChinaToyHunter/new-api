package ratio_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatMatchingModelNameDoesNotStripBase(t *testing.T) {
	assert.Equal(t, "qwen3-max@thinking:on", FormatMatchingModelName("qwen3-max@thinking:on"))
	assert.Equal(t, "claude-3-7-sonnet-thinking", FormatMatchingModelName("claude-3-7-sonnet-thinking"))
	assert.Equal(t, "gemini-2.5-flash-thinking-*", FormatMatchingModelName("gemini-2.5-flash-thinking-8192"))
	assert.Equal(t, "gpt-4-gizmo-*", FormatMatchingModelName("gpt-4-gizmo-abc"))
}

func TestRoutingMatchModelNameStripsThenWildcards(t *testing.T) {
	assert.Equal(t, "qwen3-max", RoutingMatchModelName("qwen3-max@thinking:on@temperature:0.2"))
	assert.Equal(t, "claude-3-7-sonnet", RoutingMatchModelName("claude-3-7-sonnet-thinking"))
	assert.Equal(t, "gemini-2.5-flash-thinking-*", RoutingMatchModelName("gemini-2.5-flash-thinking-8192"))
	assert.Equal(t, "gpt-5.1-codex-max", RoutingMatchModelName("gpt-5.1-codex-max"))

	geminiSettings := model_setting.GetGeminiSettings()
	old := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = old })
	assert.Equal(t, "gemini-2.5-flash", RoutingMatchModelName("gemini-2.5-flash-thinking-8192"))
}

func TestRoutingMatchModelNamePreservesExemptAtName(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })
	settings.ThinkingModelBlacklist = append(original, "re:.*@sha256:.*")

	assert.Equal(t, "opaque@sha256:deadbeef", RoutingMatchModelName("opaque@sha256:deadbeef"))
	assert.Equal(t, "kimi-k2-thinking", RoutingMatchModelName("kimi-k2-thinking"))
}

func TestUpdateGroupGroupRatioRejectsNegativeAndPreservesExistingRatios(t *testing.T) {
	original := GroupGroupRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, UpdateGroupGroupRatioByJSONString(original)) })
	require.NoError(t, UpdateGroupGroupRatioByJSONString(`{"default":{"default":1.25}}`))

	err := UpdateGroupGroupRatioByJSONString(`{"default":{"default":-0.5}}`)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "group-group ratio must be not less than 0: default/default")
	assert.JSONEq(t, `{"default":{"default":1.25}}`, GroupGroupRatio2JSONString())
}

func TestUpdateGroupGroupRatioAcceptsZero(t *testing.T) {
	original := GroupGroupRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, UpdateGroupGroupRatioByJSONString(original)) })

	require.NoError(t, UpdateGroupGroupRatioByJSONString(`{"free":{"default":0}}`))
	ratio, ok := GetGroupGroupRatio("free", "default")
	assert.True(t, ok)
	assert.Zero(t, ratio)
}

func TestUpdateGroupRatioRejectsNegativeAndPreservesExistingRatios(t *testing.T) {
	original := GroupRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, UpdateGroupRatioByJSONString(original)) })
	require.NoError(t, UpdateGroupRatioByJSONString(`{"default":1.25}`))

	err := UpdateGroupRatioByJSONString(`{"default":-0.5}`)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "group ratio must be not less than 0: default")
	assert.JSONEq(t, `{"default":1.25}`, GroupRatio2JSONString())
}
