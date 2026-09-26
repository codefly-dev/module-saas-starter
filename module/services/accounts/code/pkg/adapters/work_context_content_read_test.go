package adapters

import (
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// withDeclaredContent installs a composition declaring one module whose content
// is governed by the given permission resource types.
func withDeclaredContent(t *testing.T, resources ...string) {
	t.Helper()
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	svc.SetModulePrincipals(business.ModulePrincipalRegistry{
		business.ModulePrincipalID("example"): {Prefix: "example", Resources: resources},
	})
	service = svc
	t.Cleanup(func() { service = previous })
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
	})
	require.NoError(t, err)
	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "example.content", Action: "read", ContentRead: true},
		{ResourceKind: "example.content", Action: "write"},
		{ResourceKind: "example.content", Action: "read", ResourceID: "node:one"},
		{ResourceKind: "example.undeclared", Action: "read"},
	}, permissions)
}

// The recheck a consumer runs before honoring a capability resolves the same
// rule the mint did, or a capability minted on a collection grant would be
// rejected as stale on its first use.
func TestRecheckPermissionsFlagContentReadsLikeTheMint(t *testing.T) {
	withDeclaredContent(t, "example.content")

	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "example.content", Action: "read", ContentRead: true},
		{ResourceKind: "example.content", Action: "ingest"},
	}, permissionsFromCoreScopes([]*basev0.WorkScopeV1{
		{ResourceKind: "example.content", Actions: []string{"read", "ingest"}},
	}))
}

// A composition that declares no content flags nothing: the host governs no
// type per node, so no node-level grant can stand in for an organization role.
func TestNoDeclaredContentFlagsNothing(t *testing.T) {
	withDeclaredContent(t)

	permissions, _, err := workContextScopes([]*gen.WorkContextScope{
		{ResourceKind: "example.content", Actions: []string{"read"}},
	})
	require.NoError(t, err)
	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "example.content", Action: "read"},
	}, permissions)
}
