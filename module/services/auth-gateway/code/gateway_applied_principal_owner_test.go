package main

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type principalWitnessKeys map[string]ed25519.PublicKey

func (k principalWitnessKeys) PublicKeys(context.Context) (map[string]ed25519.PublicKey, error) {
	return k, nil
}

// This is the real owner's Issue/Check contract, not a mock that accepts any
// audience. It exercises the principal returned by the gateway's applied cache;
// the route alias must not substitute for that principal in either operation.
func TestAppliedPrincipalAddressesTheRealAuthority(t *testing.T) {
	b := principalBinding(t, "example-runtime", 1)
	cache := newSolutionRegistryCache(&principalRegistry{snapshot: principalSnapshot(b, b.Registration)})
	cache.resolveEndpoint = func(context.Context, moduleEndpoint) (string, error) { return "http://localhost:8080", nil }
	require.NoError(t, cache.refresh(t.Context()))
	route, resolution := cache.resolveRouting(t.Context(), b.Registration.SolutionId)
	require.Equal(t, solutionRoutable, resolution)
	require.NotEqual(t, route.Alias, route.Principal)
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	store := memstore.New()
	store.PutOrganization(domain.Organization{ID: "acme", Name: "Acme"})
	store.PutPerson(domain.Person{ID: "admin", Organization: "acme", Roles: []string{"admin"}})
	store.PutOrgRole(domain.OrgRole{Organization: "acme", Name: "admin"})
	svc, err := authority.New(store, authority.Config{Issuer: "https://authority.example.test", KeyID: "test", Signer: private, Keys: principalWitnessKeys{"test": public}, Replay: workcontext.NewMemoryReplayStore()})
	require.NoError(t, err)
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "example.note", Actions: []string{"read"}}}
	_, err = svc.ApplyModule(t.Context(), domain.Module{Name: route.Alias, Principal: route.Principal, Kind: domain.KindSolution, Generation: 1, OwnerAware: true, ResourceKinds: scopes, Roles: map[string][]*basev0.WorkScopeV1{domain.InstallerRole: scopes, "viewer": scopes}})
	require.NoError(t, err)
	person := authority.Person{ID: "admin", Organization: "acme", SessionID: "example-session"}
	approval, err := svc.Review(t.Context(), person, "acme", route.Alias)
	require.NoError(t, err)
	approval.OperationID = uuid.NewString()
	inst, err := svc.Install(t.Context(), person, "acme", route.Alias, "viewer", person.ID, &approval, nil)
	require.NoError(t, err)
	_, err = svc.GrantRole(t.Context(), person, authority.RoleRequest{Organization: "acme", InstallationID: inst.ID, Role: "viewer", PersonID: person.ID})
	require.NoError(t, err)
	issued, err := svc.IssueForPerson(t.Context(), person, authority.IssueRequest{Audience: route.Principal})
	require.NoError(t, err)
	require.Equal(t, route.Principal, issued.Context.GetAudience())
	_, _, err = svc.Check(t.Context(), issued.Token, route.Principal)
	require.NoError(t, err)
	_, err = svc.IssueForPerson(t.Context(), person, authority.IssueRequest{Audience: route.Alias})
	require.Error(t, err, "the route alias is not the authority principal")
	_, _, err = svc.Check(t.Context(), issued.Token, route.Alias)
	require.Error(t, err, "a capability for the principal is not addressed to the alias")
}
