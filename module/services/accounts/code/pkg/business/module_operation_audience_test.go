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

func TestInstalledOperationAudienceHeadlessScopes(t *testing.T) {
	invoke := []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"invoke", "read"}, ResourceIDs: []string{"p-1", "p-2"}}}
	lookup := []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"p-1"}}}
	binding := func(headless []ModuleOperationScope) map[string]ModuleOperationAudience {
		return map[string]ModuleOperationAudience{"model": {Audience: "other", InvokeScopes: invoke, LookupScopes: lookup, HeadlessScopes: headless}}
	}

	// Absent is valid and means "not mintable headless"; a subset of the invoke
	// grant is valid.
	require.NoError(t, validateOperationAudiences("example", binding(nil)))
	require.NoError(t, validateOperationAudiences("example", binding([]ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"invoke"}, ResourceIDs: []string{"p-2"}}})))

	for name, headless := range map[string][]ModuleOperationScope{
		"present but empty":             {},
		"action beyond invoke":          {{ResourceKind: "profiles", Actions: []string{"delete"}, ResourceIDs: []string{"p-1"}}},
		"resource beyond invoke":        {{ResourceKind: "profiles", Actions: []string{"invoke"}, ResourceIDs: []string{"p-3"}}},
		"kind-wide where invoke is not": {{ResourceKind: "profiles", Actions: []string{"invoke"}}},
		"kind beyond invoke":            {{ResourceKind: "other", Actions: []string{"invoke"}}},
		"wildcard action":               {{ResourceKind: "profiles", Actions: []string{"*"}, ResourceIDs: []string{"p-1"}}},
		"non-canonical order":           {{ResourceKind: "profiles", Actions: []string{"read", "invoke"}, ResourceIDs: []string{"p-1"}}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateOperationAudiences("example", binding(headless)))
		})
	}

	raw := `{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"model":{"audience":"other",` +
		`"invoke_scopes":[{"resource_kind":"profiles","actions":["invoke"]}],` +
		`"lookup_scopes":[{"resource_kind":"profiles","actions":["read"]}],` +
		`"headless_scopes":[{"resource_kind":"profiles","actions":["invoke"],"resource_ids":["p-1"]}]}}}}`
	_, err := ParseModulePrincipalRegistry(raw)
	require.Error(t, err, "lookup scopes must still be a subset of invoke scopes")

	raw = `{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"model":{"audience":"other",` +
		`"invoke_scopes":[{"resource_kind":"profiles","actions":["invoke","read"]}],` +
		`"lookup_scopes":[{"resource_kind":"profiles","actions":["read"]}],` +
		`"headless_scopes":[{"resource_kind":"profiles","actions":["invoke"],"resource_ids":["p-1"]}]}}}}`
	registry, err := ParseModulePrincipalRegistry(raw)
	require.NoError(t, err)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"invoke"}, ResourceIDs: []string{"p-1"}}},
		registry[ModulePrincipalID("example")].OperationAudiences["model"].HeadlessScopes)
}

func TestModuleAuthorizeOperationContext(t *testing.T) {
	registry, err := ParseModulePrincipalRegistry(`{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{` +
		`"model":{"audience":"other","invoke_scopes":[{"resource_kind":"profiles","actions":["invoke","read"]}],` +
		`"lookup_scopes":[{"resource_kind":"profiles","actions":["read"]}],` +
		`"headless_scopes":[{"resource_kind":"profiles","actions":["invoke"],"resource_ids":["p-1"]}]},` +
		`"interactive":{"audience":"other","invoke_scopes":[{"resource_kind":"profiles","actions":["invoke","read"]}],` +
		`"lookup_scopes":[{"resource_kind":"profiles","actions":["read"]}]}}}}`)
	require.NoError(t, err)
	secrets, err := ParseRegistrationSecrets("example:" + declarationDigest("example-secret"))
	require.NoError(t, err)
	s := &Service{modulePrincipals: registry}
	s.SetModuleIdentitySecrets(secrets)

	authority, err := s.ModuleAuthorizeOperationContext("example", "example-secret", "model")
	require.NoError(t, err)
	require.Equal(t, ModulePrincipalID("example"), authority.PrincipalID)
	require.Equal(t, "019f6bf7-5b4b-74e5-8c17-092259bb1661", authority.Tenant)
	require.Equal(t, "other", authority.Audience)
	require.Equal(t, "model", authority.BindingID)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"invoke"}, ResourceIDs: []string{"p-1"}}}, authority.Scopes)
	require.Equal(t, []string{"profiles:invoke:p-1"}, operationScopeGrants(authority.Scopes))

	// The returned grant is a copy: mutating it cannot widen the declared policy.
	authority.Scopes[0].Actions[0] = "delete"
	again, err := s.ModuleAuthorizeOperationContext("example", "example-secret", "model")
	require.NoError(t, err)
	require.Equal(t, []string{"invoke"}, again.Scopes[0].Actions)

	_, err = s.ModuleAuthorizeOperationContext("example", "example-secret", "interactive")
	require.ErrorIs(t, err, ErrModuleOperationContextRefused)
	_, err = s.ModuleAuthorizeOperationContext("example", "example-secret", "absent")
	require.ErrorIs(t, err, ErrModuleOperationContextRefused)
	_, err = s.ModuleAuthorizeOperationContext("example", "guessed", "model")
	require.ErrorIs(t, err, ErrModuleRegistrationDenied)
	_, err = s.ModuleAuthorizeOperationContext("unknown", "example-secret", "model")
	require.ErrorIs(t, err, ErrModuleRegistrationDenied)
}
