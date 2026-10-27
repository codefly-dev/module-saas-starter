//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// A resource question lists the ids a principal's resource-limited
// assignments name for the asked kind: an id no node places, as the mint reads
// it, and a node placing a resource of that kind; never a node of another
// kind, which a collection-scoped assignment would otherwise be answered as.
func TestScopedResourceIDsNameOnlyResourcesOfTheAskedKind(t *testing.T) {
	const kind = "example.doc"
	org, principal, role := layeredFixture(t, kind, "read")
	registerNode(t, org, "root", "root", "", "")
	registerNode(t, org, "root.col", "collection", "example.collection", "col-1")
	registerNode(t, org, "root.col.doc", "record", kind, "doc-2")
	for _, scope := range []string{"doc-1", "col-1", "doc-2"} {
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			return testStore.AssignRole(ctx, &gen.RoleAssignment{
				Id: business.NewIDString(), OrgId: org, SubjectId: principal,
				SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: role, Scope: scope,
			})
		}))
	}

	ids, err := testStore.WorkContextScopedResourceIDs(auth.WithVerifiedDatabaseIdentity(testCtx, principal, org), org, principal, kind, "read")
	require.NoError(t, err)
	require.Equal(t, []string{"doc-1", "doc-2"}, ids)
}
