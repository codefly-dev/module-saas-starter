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

const contentKind = "example.content"

// contentReadFixture is an organization with one collection node and a role
// conferring read on a declared content type, and a plain member who holds no
// organization-level role at all.
func contentReadFixture(t *testing.T) (org, member, role string) {
	t.Helper()
	org, _, role = layeredFixture(t, contentKind, "read")
	registerNode(t, org, "root", "root", "", "")
	registerNode(t, org, "root.collection", "collection", "", "")
	member = seedUser(t)
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, member, "member"))
	return org, member, role
}

func resolveContentRead(t *testing.T, org, owner string, permission business.WorkContextPermission) error {
	t.Helper()
	_, err := testStore.ResolveWorkContextAuthority(
		auth.WithVerifiedDatabaseIdentity(testCtx, owner, org), org, owner, "",
		[]business.WorkContextPermission{permission},
	)
	return err
}

func requirePermissionRefused(t *testing.T, err error, msg string) {
	t.Helper()
	require.Error(t, err, msg)
	var storeErr *business.StoreError
	require.ErrorAs(t, err, &storeErr, msg)
	require.Equal(t, business.ErrTypePermission, storeErr.StoreErrorType, msg)
}

// A member an administrator granted read on one collection may mint the
// capability to read that content — which the host then authorizes node by
// node — without any organization-level role. Before, the mint demanded an
// organization-wide role for it, so the grant the host's own UI reported as
// "Read" was unusable by every non-administrator.
func TestContentReadIsHeldThroughACollectionGrant(t *testing.T) {
	org, member, role := contentReadFixture(t)
	read := business.WorkContextPermission{ResourceKind: contentKind, Action: "read", ContentRead: true}

	requirePermissionRefused(t, resolveContentRead(t, org, member, read), "no grant anywhere confers nothing")

	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: member,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root.collection", RoleId: role})
	}))
	require.NoError(t, resolveContentRead(t, org, member, read))

	// Only the content read is widened. The same grant still does not stand in
	// for an organization role on an ordinary permission, on a resource-scoped
	// ask, or on any action other than read.
	for name, permission := range map[string]business.WorkContextPermission{
		"unflagged":       {ResourceKind: contentKind, Action: "read"},
		"resource-scoped": {ResourceKind: contentKind, Action: "read", ResourceID: "root.collection", ContentRead: true},
		"not read":        {ResourceKind: contentKind, Action: "write", ContentRead: true},
		"other type":      {ResourceKind: "example.other", Action: "read", ContentRead: true},
	} {
		requirePermissionRefused(t, resolveContentRead(t, org, member, permission), name)
	}

	// Revoking the grant withdraws it on the next mint and on every recheck.
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, org, member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "root.collection", role)
	}))
	requirePermissionRefused(t, resolveContentRead(t, org, member, read), "a revoked grant confers nothing")
}

// A team's grant reaches its members, as it does in every read oracle.
func TestContentReadIsHeldThroughATeamGrant(t *testing.T) {
	org, member, role := contentReadFixture(t)
	team := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		if err := testStore.CreateTeam(ctx, &gen.Team{Id: team, OrgId: org, Name: "Readers", Slug: "readers-" + team, Path: "readers-" + team}); err != nil {
			return err
		}
		if err := testStore.AddTeamMember(ctx, team, member, "member"); err != nil {
			return err
		}
		return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: team,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM, ScopePath: "root.collection", RoleId: role})
	}))
	require.NoError(t, resolveContentRead(t, org, member,
		business.WorkContextPermission{ResourceKind: contentKind, Action: "read", ContentRead: true}))
}

// An actor's authority is never widened: a delegated agent still needs its own
// organization-level role, whatever grants its owner holds.
func TestContentReadDoesNotWidenAnActor(t *testing.T) {
	org, member, role := contentReadFixture(t)
	agent := seedAgentPrincipal(t, org, "test.codefly.dev/content-read:0.1.0")
	for _, subject := range []string{member, agent.ID} {
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: subject,
				SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root.collection", RoleId: role})
		}))
	}
	_, err := testStore.ResolveWorkContextAuthority(
		auth.WithVerifiedDatabaseIdentity(testCtx, member, org), org, member, agent.ID,
		[]business.WorkContextPermission{{ResourceKind: contentKind, Action: "read", ContentRead: true}},
	)
	requirePermissionRefused(t, err, "an actor holds no content read through a node grant")
}

// shareContentRecord shares one record of the declared content type with the
// member. ShareRecord writes resource_id verbatim and never checks that a node
// carries it, which is what makes the placed/unplaced distinction below real.
func shareContentRecord(t *testing.T, org, member, role, recordID string) {
	t.Helper()
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.ShareRecord(ctx, &gen.RecordShare{Id: business.NewIDString(), OrgId: org,
			ResourceType: contentKind, ResourceId: recordID,
			SubjectId: member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: role})
	}))
}

// A record share is standing to mint only when it names a record this tenant
// has actually placed.
//
// Before the scope_nodes join, the share arm matched on resource_type alone: a
// share naming any string at all — a typo, an id from another system, a record
// deleted downstream — conferred permanent standing to mint an unscoped read of
// that whole content type, while every read of it was refused node by node.
// That is precisely the "the UI says Read and every read is refused" split the
// content-read branch exists to close, reintroduced through the other arm.
func TestContentReadThroughARecordShareRequiresAPlacedRecord(t *testing.T) {
	org, member, role := contentReadFixture(t)
	read := business.WorkContextPermission{ResourceKind: contentKind, Action: "read", ContentRead: true}

	shareContentRecord(t, org, member, role, "record-that-was-never-placed")
	requirePermissionRefused(t, resolveContentRead(t, org, member, read),
		"a share naming no placed record authorizes no read, so it confers no standing to ask")

	registerNode(t, org, "root.collection.record", "record", contentKind, "record-a")
	shareContentRecord(t, org, member, role, "record-a")
	require.NoError(t, resolveContentRead(t, org, member, read),
		"a share on a placed record is standing the read oracles honour")

	// Revoking it withdraws the standing again, leaving the unplaced share —
	// which must still confer nothing.
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.RevokeShare(ctx, org, contentKind, "record-a", member,
			gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, role)
	}))
	requirePermissionRefused(t, resolveContentRead(t, org, member, read), "a revoked share confers nothing")
}

// Platform read authority is the third basis, and it reaches a plain member who
// holds no grant anywhere — the same authority the read oracles already honour
// for a platform administrator. It stays narrow on its own axes: `super_admin`
// only, `read` only, and only when the adapters flagged the permission.
func TestContentReadIsHeldThroughPlatformReadAuthority(t *testing.T) {
	org, member, _ := contentReadFixture(t)
	read := business.WorkContextPermission{ResourceKind: contentKind, Action: "read", ContentRead: true}

	requirePermissionRefused(t, resolveContentRead(t, org, member, read), "no basis at all confers nothing")

	grantPlatformRoleForTest(t, member, "super_admin")
	require.NoError(t, resolveContentRead(t, org, member, read))

	for name, permission := range map[string]business.WorkContextPermission{
		"not read":  {ResourceKind: contentKind, Action: "write", ContentRead: true},
		"unflagged": {ResourceKind: contentKind, Action: "read"},
	} {
		requirePermissionRefused(t, resolveContentRead(t, org, member, permission), name)
	}
}

// Only `super_admin`. `support` and `billing` gain nothing here, exactly as they
// gain nothing in the read oracles this branch mirrors.
func TestContentReadIsNotHeldThroughANonReadPlatformRole(t *testing.T) {
	org, member, _ := contentReadFixture(t)
	grantPlatformRoleForTest(t, member, "support")
	requirePermissionRefused(t,
		resolveContentRead(t, org, member,
			business.WorkContextPermission{ResourceKind: contentKind, Action: "read", ContentRead: true}),
		"a support platform role is not platform read authority")
}
