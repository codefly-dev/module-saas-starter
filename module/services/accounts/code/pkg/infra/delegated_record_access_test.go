//go:build !pure

package infra_test

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDelegatedRecordAccessUsesRealPlacementAndAllSubjects(t *testing.T) {
	org, owner, role := layeredFixture(t, "example.record", "read")
	actor := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, actor, "member"))
	registerNode(t, org, "root", "root", "", "")
	registerNode(t, org, "root.allowed", "collection", "", "")
	registerNode(t, org, "root.private", "collection", "", "")
	registerNode(t, org, "root.allowed.record", "record", "example.record", "record-a")
	registerNode(t, org, "root.private.record", "record", "example.record", "record-b")
	for _, subject := range []string{owner, actor} {
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: subject, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root.allowed", RoleId: role})
		}))
	}
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	ctx := auth.WithVerifiedDatabaseIdentity(testCtx, owner, org)
	current := func(context.Context) error { return nil }
	allowed, err := svc.CheckDelegatedRecordAccess(ctx, org, []string{owner, actor}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.True(t, allowed.GetAllowed())
	for _, id := range []string{"record-b", "unplaced"} {
		allowed, err = svc.CheckDelegatedRecordAccess(ctx, org, []string{owner, actor}, "example.record", id, "read", current)
		require.NoError(t, err)
		require.False(t, allowed.GetAllowed())
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, org, actor, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "root.allowed", role)
	}))
	allowed, err = svc.CheckDelegatedRecordAccess(ctx, org, []string{owner, actor}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.False(t, allowed.GetAllowed())
	allowed, err = svc.CheckDelegatedRecordAccess(ctx, org, []string{owner}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.True(t, allowed.GetAllowed())
	_, err = svc.CheckDelegatedRecordAccess(ctx, business.NewIDString(), []string{owner}, "example.record", "record-a", "read", current)
	require.Error(t, err)
}
