package business

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// fencedEnvelopes stands in for an envelope store, so the consent decision's
// fence has a committed revision to compare with; it is never read here.
type fencedEnvelopes struct{ ModuleEnvelopeStore }

// The authority core reads a module installation's consent through
// moduleConsent, decided as the native mint decides it: the module's grant
// within the envelope, intersected with what the administrator consented to.
func TestAModuleInstallationsConsentIsWhatTheCoreIssuesWithin(t *testing.T) {
	const prefix = "reports"
	read := []ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"read"}}}
	readWrite := []ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"read", "write"}}}
	grant := ModulePrincipalGrant{Prefix: prefix, CrossTenant: true, OperationAudiences: map[string]ModuleOperationAudience{
		"model": {Audience: "model", HeadlessScopes: readWrite},
	}}
	consentOf := func(t *testing.T, s InstallationScope) []byte {
		raw, err := json.Marshal(s)
		require.NoError(t, err)
		return raw
	}
	for _, tc := range []struct {
		name     string
		consent  InstallationScope
		active   bool
		bindings map[string][]string // binding -> headless actions
	}{
		{name: "no capability consented", consent: InstallationScope{Capabilities: false}},
		{name: "a following consent with no envelope awaits re-consent", consent: InstallationScope{Capabilities: true, Follows: true}},
		{name: "the binding consented", active: true, consent: InstallationScope{Capabilities: true, OperationAudiences: map[string]ModuleOperationAudience{
			"model": {Audience: "model", HeadlessScopes: readWrite}}}, bindings: map[string][]string{"model": {"read", "write"}}},
		{name: "the binding consented narrower", active: true, consent: InstallationScope{Capabilities: true, OperationAudiences: map[string]ModuleOperationAudience{
			"model": {Audience: "model", HeadlessScopes: read}}}, bindings: map[string][]string{"model": {"read"}}},
		{name: "the binding dropped from the consent", active: true, consent: InstallationScope{Capabilities: true}, bindings: map[string][]string{}},
		{name: "the binding consented to another audience", active: true, consent: InstallationScope{Capabilities: true, OperationAudiences: map[string]ModuleOperationAudience{
			"model": {Audience: "elsewhere", HeadlessScopes: readWrite}}}, bindings: map[string][]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{}
			s.SetModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(prefix): grant})
			bindings, active, err := s.moduleConsent(prefix, consentOf(t, tc.consent), 0)
			require.NoError(t, err)
			require.Equal(t, tc.active, active)
			if !tc.active {
				return
			}
			require.Len(t, bindings, len(tc.bindings))
			for id, actions := range tc.bindings {
				require.Len(t, bindings[id].HeadlessScopes, 1)
				require.ElementsMatch(t, actions, bindings[id].HeadlessScopes[0].GetActions())
			}
		})
	}

	t.Run("a module the registry does not hold is allowed no binding", func(t *testing.T) {
		s := &Service{}
		s.SetModulePrincipals(ModulePrincipalRegistry{})
		bindings, active, err := s.moduleConsent(prefix, consentOf(t, InstallationScope{Capabilities: true}), 0)
		require.NoError(t, err)
		require.True(t, active)
		require.Empty(t, bindings)
	})

	t.Run("a registry derived from an older envelope than the one committed refuses", func(t *testing.T) {
		s := &Service{}
		s.SetModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(prefix): grant})
		s.SetModuleEnvelopeStore(fencedEnvelopes{})
		_, _, err := s.moduleConsent(prefix, consentOf(t, InstallationScope{Capabilities: true}), 3)
		require.True(t, errors.Is(err, ErrModuleRegistryStale), "%v", err)
	})
}
