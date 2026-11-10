//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"
)

func principalRevision(t *testing.T, orgID, principalID string) int64 {
	t.Helper()
	var r int64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx,
			`SELECT COALESCE((SELECT revision FROM public.principal_authorization_revisions WHERE org_id = $1 AND principal_id = $2), 0)`,
			orgID, principalID).Scan(&r)
	}))
	return r
}

// Revoking a scope grant moves the organization's authorization revision and
// the person's, which is what ends a capability issued while the grant stood
// (the authority core reads a scope grant as an organization role). Granting
// moves neither: more access ends nothing that was issued.
func TestRevokingAScopeGrantMovesTheAuthorizationRevision(t *testing.T) {
	orgID, principalID, roleID := layeredFixture(t, "doc", "read")
	member := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, member, "member"))

	registerNode(t, orgID, "root", "root", "", "")
	before := authorizationRevision(t, orgID)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{
			Id: business.NewIDString(), OrgId: orgID, SubjectId: member,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root",
			RoleId: roleID, GrantedBy: principalID,
		})
	}))
	require.Equal(t, before, authorizationRevision(t, orgID), "granting moves no revision")

	orgBefore, personBefore := authorizationRevision(t, orgID), principalRevision(t, orgID, member)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, orgID, member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "root", roleID)
	}))
	require.Greater(t, authorizationRevision(t, orgID), orgBefore, "revoking a scope grant moves the organization's revision")
	require.Greater(t, principalRevision(t, orgID, member), personBefore, "and the person's")

	// Revoking nothing changes nothing.
	orgAfter := authorizationRevision(t, orgID)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, orgID, member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "root", roleID)
	}))
	require.Equal(t, orgAfter, authorizationRevision(t, orgID))
}
