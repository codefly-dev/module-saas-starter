package adapters

import (
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// withComposition installs a composition declaring one module per entry, each
// keyed by the prefix a capability names as its audience.
func withComposition(t *testing.T, modules map[string][]string) {
	t.Helper()
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	registry := business.ModulePrincipalRegistry{}
	for prefix, resources := range modules {
		registry[business.ModulePrincipalID(prefix)] = business.ModulePrincipalGrant{
			Prefix: prefix, Resources: resources,
		}
	}
	svc.SetModulePrincipals(registry)
	service = svc
	t.Cleanup(func() { service = previous })
}

func withDeclaredContent(t *testing.T, resources ...string) {
	t.Helper()
	withComposition(t, map[string][]string{"example": resources})
}

// Only an unscoped read of a declared content type is a content read — the one
// permission the store may admit on a node-level grant. Every other shape keeps
// requiring an organization-level role, exactly as before.
func TestWorkContextScopesFlagOnlyUnscopedReadsOfDeclaredContent(t *testing.T) {
	withDeclaredContent(t, "example.content")

	permissions, _, err := workContextScopes([]*gen.WorkContextScope{
		{ResourceKind: "example.content", Actions: []string{"read", "write"}},
		{ResourceKind: "example.content", Actions: []string{"read"}, ResourceIds: []string{"node:one"}},
		{ResourceKind: "example.undeclared", Actions: []string{"read"}},
	}, mintContentReads("example"))
	require.NoError(t, err)
	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "example.content", Action: "read", ContentRead: true},
		{ResourceKind: "example.content", Action: "write"},
		{ResourceKind: "example.content", Action: "read", ResourceID: "node:one"},
		{ResourceKind: "example.undeclared", Action: "read"},
	}, permissions)
}

// A mint admits only the content the AUDIENCE declares, never the composition's
// union. Reading the union here would let one module's declaration decide what a
// capability minted for a different module may carry — and would seal a scope
// the audience's own read RPCs then refuse, since both gate on
// ModuleContentResources(audience).
func TestMintFlagsOnlyTheAudiencesOwnContent(t *testing.T) {
	withComposition(t, map[string][]string{
		"alpha": {"alpha.docs"},
		"beta":  {"beta.rows"},
	})

	permissions, _, err := workContextScopes([]*gen.WorkContextScope{
		{ResourceKind: "alpha.docs", Actions: []string{"read"}},
		{ResourceKind: "beta.rows", Actions: []string{"read"}},
	}, mintContentReads("alpha"))
	require.NoError(t, err)
	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "alpha.docs", Action: "read", ContentRead: true},
		{ResourceKind: "beta.rows", Action: "read"},
	}, permissions, "a sibling module's content is not this audience's to mint")
}

// An audience no composed module registered, and one that declares no content,
// both admit nothing (fail-closed).
func TestMintFlagsNothingForAnUndeclaredAudience(t *testing.T) {
	withComposition(t, map[string][]string{
		"alpha": {"alpha.docs"},
		"empty": nil,
	})

	for _, audience := range []string{"", "stranger", "empty"} {
		permissions, _, err := workContextScopes([]*gen.WorkContextScope{
			{ResourceKind: "alpha.docs", Actions: []string{"read"}},
		}, mintContentReads(audience))
		require.NoError(t, err)
		require.Equal(t, []business.WorkContextPermission{
			{ResourceKind: "alpha.docs", Action: "read"},
		}, permissions, "audience %q must mint no content read", audience)
	}
}

// A composition that declares no content flags nothing: the host governs no
// type per node, so no node-level grant can stand in for an organization role.
func TestNoDeclaredContentFlagsNothing(t *testing.T) {
	withDeclaredContent(t)

	permissions, _, err := workContextScopes([]*gen.WorkContextScope{
		{ResourceKind: "example.content", Actions: []string{"read"}},
	}, mintContentReads("example"))
	require.NoError(t, err)
	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "example.content", Action: "read"},
	}, permissions)
}

// A recheck is the superset of every mint: it admits every unscoped read and
// reads no declaration. A recheck narrower than the mint that issued a
// capability would report a live capability as revoked.
func TestRecheckAdmitsEveryUnscopedRead(t *testing.T) {
	withDeclaredContent(t, "example.content")

	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "example.content", Action: "read", ContentRead: true},
		{ResourceKind: "example.content", Action: "ingest"},
		{ResourceKind: "example.undeclared", Action: "read", ContentRead: true},
		{ResourceKind: "example.content", Action: "read", ResourceID: "node:one"},
	}, permissionsFromCoreScopes([]*basev0.WorkScopeV1{
		{ResourceKind: "example.content", Actions: []string{"read", "ingest"}},
		{ResourceKind: "example.undeclared", Actions: []string{"read"}},
		{ResourceKind: "example.content", Actions: []string{"read"}, ResourceIds: []string{"node:one"}},
	}, recheckContentReads()))
}

// The regression that motivated splitting the two rules: MODULE_PRINCIPALS is
// read once at process start, so a host mid-rollout may hold a declaration that
// the host which minted a capability did not. A recheck must not consult it —
// otherwise that host refuses a capability minted seconds earlier and its
// consumer reads the refusal as a revocation.
func TestRecheckIsIndependentOfTheDeclaration(t *testing.T) {
	withComposition(t, map[string][]string{})

	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "example.content", Action: "read", ContentRead: true},
	}, permissionsFromCoreScopes([]*basev0.WorkScopeV1{
		{ResourceKind: "example.content", Actions: []string{"read"}},
	}, recheckContentReads()),
		"a host that declares nothing must still honour a capability another host minted")
}
