package business

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

// Reuse the selected owner's issuance and memory store, following authority's
// TestAnInstallationsConsentBoundsItsModulesBindings. Lookup goes through
// IssueChild -> decide.ScopesFor, not an inferred invoke alias. This pure
// witness does not qualify the database adapter or the served route.
func TestReadinessSelfOperationsMatchOwnerConsent(t *testing.T) {
	ctx := context.Background()
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	store := memstore.New()
	owner, err := authority.New(store, authority.Config{
		Issuer: "https://authority.example.com", KeyID: "test", Signer: private,
		Keys: staticKeys{"test": public}, Replay: workcontext.NewMemoryReplayStore(), HostInstallsModules: true,
	})
	require.NoError(t, err)
	read := []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}}
	write := []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"write"}}}
	readWrite := []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read", "write"}}}
	store.PutOrganization(domain.Organization{ID: "acme", Name: "Acme"})
	store.PutOrgRole(domain.OrgRole{Organization: "acme", Name: "reader", Scopes: []domain.RoleScope{{Kind: "evidence", Actions: []string{"read"}}}})
	store.PutPerson(domain.Person{ID: "jane", Organization: "acme", Roles: []string{"reader"}})
	for _, name := range []string{readinessPrefix, "model"} {
		_, err := owner.ApplyModule(ctx, domain.Module{Name: name, Principal: name, Kind: domain.KindModule, Generation: 1,
			ResourceKinds: readWrite,
			Bindings: map[string]domain.Binding{
				"self":         {ID: "self", Audience: name, InvokeScopes: readWrite, LookupScopes: read, HeadlessScopes: read},
				"lookup-write": {ID: "lookup-write", Audience: name, InvokeScopes: readWrite, LookupScopes: write},
				"outward":      {ID: "outward", Audience: "model", InvokeScopes: read, LookupScopes: read},
			},
		})
		require.NoError(t, err)
	}
	// The real host consent adapter allows capability issuance with no outward
	// bindings; removing capability consent still returns inactive.
	host := &Service{}
	host.SetModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(readinessPrefix): {Prefix: readinessPrefix}})
	bindings, active, err := host.moduleConsent(readinessPrefix, []byte(`{"capabilities":true}`), 0)
	require.NoError(t, err)
	require.True(t, active)
	require.Empty(t, bindings)
	for _, name := range []string{readinessPrefix, "model"} {
		require.NoError(t, store.Write(ctx, func(tx authority.Tx) error {
			return tx.PutInstallation(domain.Installation{ID: "installation-" + name, Organization: "acme", Target: name,
				Status: domain.Active, Revision: 1, Bindings: bindings})
		}))
	}
	person := authority.Person{ID: "jane", Organization: "acme", SessionID: "session"}
	parent, err := owner.IssueForPerson(ctx, person, authority.IssueRequest{Audience: readinessPrefix})
	require.NoError(t, err)
	actor := authority.Workload{Module: readinessPrefix}
	for _, operation := range []string{"invoke", "lookup"} {
		t.Run(operation, func(t *testing.T) {
			child, err := owner.IssueChild(ctx, parent.Token, actor, authority.IssueRequest{Binding: "self", Operation: operation})
			require.NoError(t, err)
			verified, err := owner.Verify(ctx, child.Token, readinessPrefix)
			require.NoError(t, err)
			require.Len(t, verified.EffectiveScopes(), 1)
			require.Equal(t, []string{"read"}, verified.EffectiveScopes()[0].GetActions())
			_, err = owner.IssueChild(ctx, parent.Token, actor, authority.IssueRequest{Binding: "self", Operation: operation, Scopes: write})
			require.Error(t, err, "self does not widen the person's parent")
			_, err = owner.IssueChild(ctx, parent.Token, actor, authority.IssueRequest{Binding: "outward", Operation: operation})
			require.ErrorContains(t, err, "consents to", "outward use still needs binding consent")
		})
	}
	_, err = owner.IssueChild(ctx, parent.Token, actor, authority.IssueRequest{Binding: "lookup-write", Operation: "lookup"})
	require.Error(t, err, "lookup uses its own write ceiling, not the readable invoke ceiling")
	_, err = owner.IssueForWorkload(ctx, actor, authority.IssueRequest{Binding: "self", Organization: "acme"})
	require.ErrorContains(t, err, "consents to", "headless self still needs binding consent")
	_, err = owner.IssueChild(ctx, parent.Token, actor, authority.IssueRequest{Binding: "missing"})
	require.Error(t, err, "an undeclared binding stays refused")
	_, active, err = host.moduleConsent(readinessPrefix, []byte(`{"capabilities":false}`), 0)
	require.NoError(t, err)
	require.False(t, active, "no self exemption from capability consent")
	// The production postgres consent adapter maps this inactive answer to
	// Pending. Exercise the owner's refusal at that existing boundary.
	require.NoError(t, store.Write(ctx, func(tx authority.Tx) error {
		return tx.PutInstallation(domain.Installation{ID: "installation-" + readinessPrefix, Organization: "acme", Target: readinessPrefix,
			Status: domain.Pending, Revision: 2, Bindings: bindings})
	}))
	_, err = owner.IssueForPerson(ctx, person, authority.IssueRequest{Audience: readinessPrefix})
	require.Error(t, err)
	_, err = owner.IssueChild(ctx, parent.Token, actor, authority.IssueRequest{Binding: "self", Operation: "lookup"})
	require.Error(t, err, "a stale parent cannot survive its installation becoming inactive")
}
