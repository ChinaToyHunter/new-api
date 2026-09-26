package jsplugin

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrySetEnabledHidesFactoryAndOverride(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.RegisterFactory(routingTestPluginSource(
		"master-factory",
		50,
		`["master-factory-model"]`,
		`routes: [
			{method: "POST", path: "/vendor/master-factory", type: "submit", action: "generate", decode: "decodeVideo", render: "videoCreated"}
		],
		protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
		`export const native = {
			decodeVideo: function(ctx) { return {kind: "submit", model: "master-factory-model", requestBody: ctx.body.value}; },
			videoCreated: function(ctx, task) { return task; }
		};
		`+routingProtocolExport("openai_responses"),
	), Options{})
	require.NoError(t, err)
	_, err = registry.Register(routingTestPluginSource(
		"master-override",
		51,
		`["master-override-model"]`,
		`routes: [
			{method: "POST", path: "/vendor/master-override", type: "submit", action: "generate", decode: "decodeVideo", render: "videoCreated"}
		],
		protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
		`export const native = {
			decodeVideo: function(ctx) { return {kind: "submit", model: "master-override-model", requestBody: ctx.body.value}; },
			videoCreated: function(ctx, task) { return task; }
		};
		`+routingProtocolExport("openai_responses"),
	), Options{})
	require.NoError(t, err)

	_, ok := registry.Get("master-factory")
	require.True(t, ok)
	_, ok = registry.Get("master-override")
	require.True(t, ok)
	before := registry.Generation().Number

	registry.SetEnabled(false)
	_, ok = registry.Get("master-factory")
	assert.False(t, ok)
	_, ok = registry.Get("master-override")
	assert.False(t, ok)
	generation := registry.Generation()
	assert.Greater(t, generation.Number, before)
	assert.Empty(t, generation.Plugins())
	assert.Empty(t, generation.Routes())
	_, ok = generation.LookupDeclaredRoute("POST", "/vendor/master-factory")
	assert.False(t, ok)
	_, ok = generation.LookupDeclaredRoute("POST", "/vendor/master-override")
	assert.False(t, ok)
	_, ok = generation.LookupEndpoint("POST", "/v1/responses", "master-factory-model")
	assert.False(t, ok)
	_, ok = generation.LookupEndpoint("POST", "/v1/responses", "master-override-model")
	assert.False(t, ok)

	snapshot := registry.Snapshot()
	require.Len(t, snapshot.Factory, 1)
	assert.Equal(t, "master-factory", snapshot.Factory[0].Key)
	require.Len(t, snapshot.Override, 1)
	assert.Equal(t, "master-override", snapshot.Override[0].Key)
}

func TestRegistryMutationsWhileDisabledDoNotResurrectEndpoints(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0-factory", true))
	registry.SetEnabled(false)
	_, ok := registry.Get("test")
	require.False(t, ok)

	require.NoError(t, registerTestPlugin(registry, "1.0.0-override", false))
	registry.SetDisabledFactoryKeys([]string{"other"})
	_, ok = registry.Get("test")
	assert.False(t, ok)
	assert.Empty(t, registry.Generation().Plugins())

	override := registry.OverridePlugins()
	require.Contains(t, override, "test")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{override["test"]}))
	_, ok = registry.Get("test")
	assert.False(t, ok)
	assert.Empty(t, registry.Generation().Plugins())
}

func TestRegistrySetEnabledTrueRestoresFactoryAndOverride(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0-factory", true))
	registry.SetEnabled(false)
	require.NoError(t, registerTestPlugin(registry, "1.0.0-override", false))
	_, ok := registry.Get("test")
	require.False(t, ok)

	registry.SetEnabled(true)
	plugin, ok := registry.Get("test")
	require.True(t, ok)
	assert.Equal(t, "1.0.0-override", plugin.Meta.Version)
	snapshot := registry.Snapshot()
	require.Len(t, snapshot.Factory, 1)
	require.Len(t, snapshot.Override, 1)
}

func TestRegistrySetEnabledNoOpDoesNotBumpGeneration(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0", true))
	before := registry.Generation().Number

	registry.SetEnabled(true)
	assert.Equal(t, before, registry.Generation().Number)
}

func TestRegistryMasterSwitchIsOrthogonalToLayerFlags(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0-factory", true))

	registry.SetDisabledFactoryKeys([]string{"test"})
	_, ok := registry.Get("test")
	assert.False(t, ok)

	registry.SetEnabled(false)
	_, ok = registry.Get("test")
	assert.False(t, ok)

	registry.SetEnabled(true)
	_, ok = registry.Get("test")
	assert.False(t, ok)

	registry.SetDisabledFactoryKeys(nil)
	plugin, ok := registry.Get("test")
	require.True(t, ok)
	assert.Equal(t, "1.0.0-factory", plugin.Meta.Version)
}

func TestRegistryOverrideLayerCanBeDisabledWithoutDiscardingDesiredOverrides(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0-factory", true))
	require.NoError(t, registerTestPlugin(registry, "1.0.0-override", false))
	before := registry.Generation()
	beforeOverride := registry.OverridePlugins()["test"]

	require.NoError(t, registry.SetOverrideEnabled(false))
	plugin, ok := registry.Get("test")
	require.True(t, ok)
	assert.Equal(t, "1.0.0-factory", plugin.Meta.Version)
	assert.False(t, registry.OverrideEnabled())
	assert.Empty(t, registry.ActiveOverridePlugins())
	assert.Same(t, beforeOverride, registry.OverridePlugins()["test"])
	assert.Greater(t, registry.Generation().Number, before.Number)

	require.NoError(t, registry.SetOverrideEnabled(true))
	plugin, ok = registry.Get("test")
	require.True(t, ok)
	assert.Equal(t, "1.0.0-override", plugin.Meta.Version)
	assert.True(t, registry.OverrideEnabled())
	assert.Same(t, registry.OverridePlugins()["test"], registry.ActiveOverridePlugins()["test"])
}

func TestRegistryOverrideLayerNoOpKeepsGeneration(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0-override", false))
	before := registry.Generation()

	require.NoError(t, registry.SetOverrideEnabled(true))
	assert.Same(t, before, registry.Generation())

	require.NoError(t, registry.SetOverrideEnabled(false))
	before = registry.Generation()
	require.NoError(t, registry.SetOverrideEnabled(false))
	assert.Same(t, before, registry.Generation())
}

func TestRegistryOverrideLayerToggleRetainsStateWhenRebuildFails(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0-factory", true))
	require.NoError(t, registerTestPlugin(registry, "1.0.0-override", false))
	shouldFail := false
	require.NoError(t, registry.SetGenerationPreparer(func(candidate, _ *RoutingGeneration) (PreparedRoutingGeneration, error) {
		if shouldFail {
			return PreparedRoutingGeneration{}, errors.New("forced override toggle failure")
		}
		return PreparedRoutingGeneration{Generation: candidate}, nil
	}))
	beforeGeneration := registry.Generation()
	beforeOverride := registry.OverridePlugins()["test"]
	beforeActive := registry.ActiveOverridePlugins()["test"]
	shouldFail = true

	err := registry.SetOverrideEnabled(false)
	require.ErrorContains(t, err, "forced override toggle failure")
	assert.True(t, registry.OverrideEnabled())
	assert.Same(t, beforeGeneration, registry.Generation())
	assert.Same(t, beforeOverride, registry.OverridePlugins()["test"])
	assert.Same(t, beforeActive, registry.ActiveOverridePlugins()["test"])
	assert.Equal(t, "failed", registry.LastRebuildOutcome().Status)
}
