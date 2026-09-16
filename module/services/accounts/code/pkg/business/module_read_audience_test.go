package business

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstalledReadAudiencePolicy(t *testing.T) {
	valid := ModuleReadAudience{Audience: "example-producer", Scopes: []ModuleReadScope{{ResourceKind: "results", ResourceIDs: []string{"result-1"}}}}
	raw, _ := json.Marshal(map[string]any{"example": map[string]any{"tenant": "019f6bf7-5b4b-74e5-8c17-092259bb1661", "read_audiences": map[string]ModuleReadAudience{"proof": valid}}})
	registry, err := ParseModulePrincipalRegistry(string(raw))
	require.NoError(t, err)
	got := registry[ModulePrincipalID("example")].ReadAudiences["proof"]
	require.Equal(t, valid, got)
	require.Equal(t, []string{"read"}, got.WireScopes()[0].Actions)
	for _, binding := range []ModuleReadAudience{{}, {Audience: "example", Scopes: valid.Scopes}, {Audience: "other"}, {Audience: "other", Scopes: []ModuleReadScope{{ResourceKind: "results"}, {ResourceKind: "results"}}}, {Audience: "other", Scopes: []ModuleReadScope{{ResourceKind: "results", ResourceIDs: []string{"one", "one"}}}}} {
		require.Error(t, validateReadAudiences("example", map[string]ModuleReadAudience{"proof": binding}))
	}
}

func TestInstalledReadAudienceRejectsRequestedActions(t *testing.T) {
	_, err := ParseModulePrincipalRegistry(`{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","read_audiences":{"proof":{"audience":"other","scopes":[{"resource_kind":"results","actions":["write"]}]}}}}`)
	require.Error(t, err)
}
