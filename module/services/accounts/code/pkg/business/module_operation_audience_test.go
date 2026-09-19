package business

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstalledOperationAudiencePolicy(t *testing.T) {
	valid := ModuleOperationAudience{
		Audience:     "example-operation",
		InvokeScopes: []ModuleOperationScope{{ResourceKind: "results", Actions: []string{"read", "write"}, ResourceIDs: []string{"result-1"}}},
		LookupScopes: []ModuleOperationScope{{ResourceKind: "results", Actions: []string{"read"}, ResourceIDs: []string{"result-1"}}},
	}
	raw, err := json.Marshal(map[string]any{"example": map[string]any{"tenant": "019f6bf7-5b4b-74e5-8c17-092259bb1661", "operation_audiences": map[string]ModuleOperationAudience{"generate": valid}}})
	require.NoError(t, err)
	registry, err := ParseModulePrincipalRegistry(string(raw))
	require.NoError(t, err)
	got := registry[ModulePrincipalID("example")].OperationAudiences["generate"]
	require.Equal(t, valid, got)
	require.Equal(t, []string{"read", "write"}, got.WireScopes(false)[0].Actions)
	require.Equal(t, []string{"read"}, got.WireScopes(true)[0].Actions)

	invalid := []ModuleOperationAudience{
		{},
		{Audience: "example", InvokeScopes: valid.InvokeScopes, LookupScopes: valid.LookupScopes},
		{Audience: "other", InvokeScopes: valid.InvokeScopes},
		{Audience: "other", InvokeScopes: valid.InvokeScopes, LookupScopes: []ModuleOperationScope{{ResourceKind: "results", Actions: []string{"write"}, ResourceIDs: []string{"result-1"}}}},
		{Audience: "other", InvokeScopes: valid.InvokeScopes, LookupScopes: []ModuleOperationScope{{ResourceKind: "results", Actions: []string{"read"}}}},
	}
	for _, binding := range invalid {
		require.Error(t, validateOperationAudiences("example", map[string]ModuleOperationAudience{"generate": binding}))
	}
}

func TestInstalledOperationAudienceRejectsUnknownFieldsAndNonCanonicalSets(t *testing.T) {
	for _, raw := range []string{
		`{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"generate":{"audience":"other","scopes":[]}}}}`,
		`{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"generate":{"audience":"other","invoke_scopes":[{"resource_kind":"results","actions":["write","read"]}],"lookup_scopes":[{"resource_kind":"results","actions":["read"]}]}}}}`,
		"{\"example\":{\"tenant\":\"019f6bf7-5b4b-74e5-8c17-092259bb1661\",\"operation_audiences\":{\"generate\":{\"audience\":\"other\",\"invoke_scopes\":[{\"resource_kind\":\"results\",\"actions\":[\"read\\n\"]}],\"lookup_scopes\":[{\"resource_kind\":\"results\",\"actions\":[\"read\"]}]}}}}",
	} {
		_, err := ParseModulePrincipalRegistry(raw)
		require.Error(t, err)
	}
}

func TestDelegatedAudienceActorTypeSpeaksTheRegisteredVocabulary(t *testing.T) {
	for name, tc := range map[string]struct {
		actor *Principal
		want  string
	}{
		"direct owner":     {nil, ActorTypeUser},
		"human delegate":   {&Principal{Kind: PrincipalKindHuman}, ActorTypeUser},
		"agent delegate":   {&Principal{Kind: PrincipalKindAgent}, ActorTypeAgent},
		"service delegate": {&Principal{Kind: PrincipalKindService}, ActorTypeSystem},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, delegatedAudienceActorType(tc.actor))
		})
	}
}
