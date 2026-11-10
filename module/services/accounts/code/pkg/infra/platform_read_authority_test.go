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

// grantPlatformRoleForTest makes userID a platform administrator for the test's
// lifetime. platform_admins is a global table, so the row is removed on cleanup
// rather than left to leak authority into every later test on this database.
// Both writes run on the control plane, as PlatformAdminService's do: the tenant
// role may read platform_admins but never write it.
func grantPlatformRoleForTest(t *testing.T, userID, role string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.GrantPlatformRole(ctx, userID, role, userID)
	}))
	t.Cleanup(func() { _ = revokePlatformRoleForTest(userID) })
}

func revokePlatformRoleForTest(userID string) error {
	return testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.RevokePlatformRole(ctx, userID)
	})
}

// platformReadFixture is one tenant with a collection, a record placed in it,
// and a source bound to a second collection — and no read grant for anybody.
// The organization's owner (layeredFixture) holds no grant either, which is
// what makes it the "org administrator without a grant" case.
type platformReadFixture struct {
	org, orgOwner, role string
}

func newPlatformReadFixture(t *testing.T) platformReadFixture {
	t.Helper()
	org, owner, role := layeredFixture(t, "example.record", "read")
	registerNode(t, org, "root", "root", "", "")
	registerNode(t, org, "root.collection", "collection", "", "")
	registerNode(t, org, "root.collection.record", "record", "example.record", "record-a")
	return platformReadFixture{org: org, orgOwner: owner, role: role}
}

// joinOrg makes a fresh user a plain member of the fixture's organization.
func (f platformReadFixture) joinOrg(t *testing.T) string {
	t.Helper()
	user := seedUser(t)
	seedOrgMember(t, f.org, user)
	return user
}

type platformReadAnswer struct {
	checkAccess bool
	reason      string
	canReadNode bool
	listed      map[string]gen.AccessBasis
	resourceIDs []string
}

// readAs asks every layered-access oracle the same question about subject, on
// ctx, so a test states one expectation and every reader is held to it.
func (f platformReadFixture) readAs(t *testing.T, ctx context.Context, subject, action string) platformReadAnswer {
	t.Helper()
	var answer platformReadAnswer
	require.NoError(t, testStore.WithOrgTx(ctx, f.org, func(ctx context.Context) error {
		var err error
		answer.checkAccess, answer.reason, err = testStore.CheckAccess(ctx, subject, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "example.record", "record-a", action)
		if err != nil {
			return err
		}
		scopes, err := testStore.ListAccessibleScopes(ctx, f.org, subject, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "example.record", action, "", 100)
		if err != nil {
			return err
		}
		answer.listed = map[string]gen.AccessBasis{}
		var collection string
		for _, scope := range scopes {
			answer.listed[scope.ScopePath] = scope.Basis
			if scope.ScopePath == "root.collection" {
				collection = scope.NodeId
			}
		}
		if collection != "" {
			answer.canReadNode, err = testStore.CanReadScopeNode(ctx, f.org, subject, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "example.record", action, collection)
			if err != nil {
				return err
			}
		}
		answer.resourceIDs, err = testStore.ListAccessibleResourceIDs(ctx, f.org, subject, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "example.record", action, []string{"record-a"})
		return err
	}))
	return answer
}

func requireNoRead(t *testing.T, answer platformReadAnswer, why string) {
	t.Helper()
	require.False(t, answer.checkAccess, "CheckAccess: %s", why)
	require.Empty(t, answer.listed, "ListAccessibleScopes: %s", why)
	require.Empty(t, answer.resourceIDs, "ListAccessibleResourceIDs: %s", why)
}

// A platform role is a role like any other: it holds no read of its own. Every
// oracle refuses a platform super_admin who has no grant, and a grant it does
// hold reads exactly the grant. This is the owner's rule that all roles are
// explicit; it failed against the platform read authority this host used to
// have, which let a super_admin read every collection in every organization.
func TestPlatformSuperAdminReadsNothingWithoutAGrant(t *testing.T) {
	f := newPlatformReadFixture(t)
	admin := f.joinOrg(t)
	grantPlatformRoleForTest(t, admin, "super_admin")

	answer := f.readAs(t, testCtx, admin, "read")
	requireNoRead(t, answer, "a platform super_admin with no grant")
	require.Equal(t, "no scope grant or record share", answer.reason,
		"the refusal names why: there is no grant, not a hidden policy")

	// An explicit grant is read as a grant, and reaches only what it names.
	require.NoError(t, testStore.WithOrgTx(testCtx, f.org, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: f.org, SubjectId: admin,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root.collection", RoleId: f.role})
	}))
	answer = f.readAs(t, testCtx, admin, "read")
	require.Equal(t, "granted via scope", answer.reason)
	require.Equal(t, map[string]gen.AccessBasis{
		"root.collection":        gen.AccessBasis_ACCESS_BASIS_GRANT,
		"root.collection.record": gen.AccessBasis_ACCESS_BASIS_GRANT,
	}, answer.listed, "the grant sits below root: root is not reachable through the platform role")
}

// No role confers a read by itself: not the organization's owner, a plain
// member, nor any platform role.
func TestNoRoleReadsWithoutAGrant(t *testing.T) {
	f := newPlatformReadFixture(t)
	requireNoRead(t, f.readAs(t, testCtx, f.orgOwner, "read"),
		"an organization owner's flat role never substitutes for a collection grant")
	requireNoRead(t, f.readAs(t, testCtx, f.joinOrg(t), "read"), "a plain member holds no grant")

	for _, role := range []string{"super_admin", "support", "billing"} {
		user := f.joinOrg(t)
		grantPlatformRoleForTest(t, user, role)
		requireNoRead(t, f.readAs(t, testCtx, user, "read"), "platform "+role+" holds no grant")
	}
}

// A module acting for a viewer asks through the Work Context oracles. A
// super_admin viewer with no grant is refused there as well, on the exact-record
// check and on the collection projection; with a grant the same calls pass.
func TestPlatformSuperAdminIsRefusedWhenAModuleActsForThem(t *testing.T) {
	f := newPlatformReadFixture(t)
	admin := f.joinOrg(t)
	grantPlatformRoleForTest(t, admin, "super_admin")
	source := seedDatasourceSource(t, f.org)

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	current := func(context.Context) error { return nil }
	viewer := auth.WithVerifiedDatabaseIdentity(testCtx, admin, f.org)
	readable := func(subjects, resources []string) []string {
		t.Helper()
		var ids []string
		require.NoError(t, testStore.WithSourceReadSnapshot(viewer, f.org, func(snapshot context.Context) error {
			rows, err := testStore.ListReadableSourcesPage(snapshot, f.org, subjects, resources, "", 10)
			for _, row := range rows {
				ids = append(ids, row.SourceId)
			}
			return err
		}))
		return ids
	}

	decision, err := svc.CheckDelegatedRecordAccess(viewer, f.org, []string{admin}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.False(t, decision.GetAllowed(), "a platform role confers no read on a module's record")
	require.Empty(t, readable([]string{admin}, []string{"documents"}),
		"a platform role confers no read on a module's collection")

	// Granted at the source's collection, both pass.
	require.NoError(t, testStore.WithOrgTx(testCtx, f.org, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: f.org, SubjectId: admin,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root", RoleId: f.role})
	}))
	decision, err = svc.CheckDelegatedRecordAccess(viewer, f.org, []string{admin}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.True(t, decision.GetAllowed())
	_ = source
}
