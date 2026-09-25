//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// grantPlatformRoleForTest makes userID a platform administrator for the test's
// lifetime. platform_admins is a global table, so the row is removed on cleanup
// rather than left to leak authority into every later test on this database.
func grantPlatformRoleForTest(t *testing.T, userID, role string) {
	t.Helper()
	require.NoError(t, testStore.GrantPlatformRole(testCtx, userID, role, userID))
	t.Cleanup(func() { _ = testStore.RevokePlatformRole(testCtx, userID) })
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

func impersonating(realActor, effectiveSubject string) context.Context {
	return auth.WithVerifiedRequestIdentity(testCtx, auth.RequestIdentity{
		RealActor:        uuid.MustParse(realActor),
		EffectiveSubject: uuid.MustParse(effectiveSubject),
	})
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

// A platform super_admin reads every node in an organization it has no grant
// in, and every oracle says the read rests on platform authority, not a grant.
func TestPlatformSuperAdminReadsWithoutAGrant(t *testing.T) {
	f := newPlatformReadFixture(t)
	admin := f.joinOrg(t)
	grantPlatformRoleForTest(t, admin, "super_admin")

	answer := f.readAs(t, testCtx, admin, "read")
	require.True(t, answer.checkAccess)
	require.Equal(t, infra.PlatformReadBasis, answer.reason)
	require.Equal(t, map[string]gen.AccessBasis{
		"root":                   gen.AccessBasis_ACCESS_BASIS_PLATFORM_ADMINISTRATOR,
		"root.collection":        gen.AccessBasis_ACCESS_BASIS_PLATFORM_ADMINISTRATOR,
		"root.collection.record": gen.AccessBasis_ACCESS_BASIS_PLATFORM_ADMINISTRATOR,
	}, answer.listed)
	require.True(t, answer.canReadNode)
	require.Equal(t, []string{"record-a"}, answer.resourceIDs)

	// Read only: platform authority confers no other action, and no wildcard.
	for _, action := range []string{"write", "delete", "*"} {
		requireNoRead(t, f.readAs(t, testCtx, admin, action), "platform authority is read-only, not "+action)
	}

	// A grant the administrator also holds is reported as the grant.
	require.NoError(t, testStore.WithOrgTx(testCtx, f.org, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: f.org, SubjectId: admin,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: "root.collection", RoleId: f.role})
	}))
	answer = f.readAs(t, testCtx, admin, "read")
	require.Equal(t, "granted via scope", answer.reason)
	require.Equal(t, gen.AccessBasis_ACCESS_BASIS_GRANT, answer.listed["root.collection"])
	require.Equal(t, gen.AccessBasis_ACCESS_BASIS_GRANT, answer.listed["root.collection.record"])
	require.Equal(t, gen.AccessBasis_ACCESS_BASIS_PLATFORM_ADMINISTRATOR, answer.listed["root"],
		"the grant sits below root, so root is still reachable only through platform authority")

	// Revoking the platform role withdraws the authority on the next read.
	require.NoError(t, testStore.RevokePlatformRole(testCtx, admin))
	answer = f.readAs(t, testCtx, admin, "read")
	require.NotContains(t, answer.listed, "root")
	require.Equal(t, gen.AccessBasis_ACCESS_BASIS_GRANT, answer.listed["root.collection"])
}

// Nothing but the super_admin platform role confers it: not the organization's
// owner, not a plain member, not the lesser platform roles.
func TestPlatformReadAuthorityIsOnlySuperAdmin(t *testing.T) {
	f := newPlatformReadFixture(t)
	requireNoRead(t, f.readAs(t, testCtx, f.orgOwner, "read"),
		"an organization owner's flat role never substitutes for a collection grant")

	member := f.joinOrg(t)
	requireNoRead(t, f.readAs(t, testCtx, member, "read"), "a plain member holds no grant")

	for _, role := range []string{"support", "billing"} {
		user := f.joinOrg(t)
		grantPlatformRoleForTest(t, user, role)
		requireNoRead(t, f.readAs(t, testCtx, user, "read"), "platform "+role+" is not an administrator of content")
	}

	// A team never holds platform authority, even one a super_admin belongs to.
	admin := f.joinOrg(t)
	grantPlatformRoleForTest(t, admin, "super_admin")
	require.NoError(t, testStore.WithOrgTx(testCtx, f.org, func(ctx context.Context) error {
		ok, _, err := testStore.CheckAccess(ctx, admin, gen.SubjectKind_SUBJECT_KIND_TEAM, "example.record", "record-a", "read")
		require.False(t, ok)
		return err
	}))
}

// Impersonation confers it in neither direction: an administrator impersonating
// a member does not read through the member, and nobody impersonating an
// administrator reads through the administrator.
func TestPlatformReadAuthorityNeverCrossesImpersonation(t *testing.T) {
	f := newPlatformReadFixture(t)
	admin := f.joinOrg(t)
	grantPlatformRoleForTest(t, admin, "super_admin")
	member := f.joinOrg(t)
	support := f.joinOrg(t)
	grantPlatformRoleForTest(t, support, "support")

	requireNoRead(t, f.readAs(t, impersonating(admin, member), member, "read"),
		"the impersonator's platform role must not reach the member it acts as")
	requireNoRead(t, f.readAs(t, impersonating(support, admin), admin, "read"),
		"the impersonated administrator's platform role is not the impersonator's to borrow")
	requireNoRead(t, f.readAs(t, impersonating(admin, member), admin, "read"),
		"no subject reads through platform authority on an impersonated request")

	// The same administrator, signed in as themselves, does read.
	require.True(t, f.readAs(t, testCtx, admin, "read").checkAccess)
}

// A module acting for a viewer asks through the Work Context oracles, which
// intersect every subject the capability names. A super_admin viewer passes on
// platform authority there as well, while a delegated actor with no grant of
// its own still fails the intersection.
func TestPlatformReadAuthorityReachesAModuleActingForTheViewer(t *testing.T) {
	f := newPlatformReadFixture(t)
	admin := f.joinOrg(t)
	grantPlatformRoleForTest(t, admin, "super_admin")
	member := f.joinOrg(t)
	source := seedDatasourceSource(t, f.org)

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	current := func(context.Context) error { return nil }

	// The exact-record oracle behind CheckWorkContextRecordAccess.
	viewer := auth.WithVerifiedDatabaseIdentity(testCtx, admin, f.org)
	decision, err := svc.CheckDelegatedRecordAccess(viewer, f.org, []string{admin}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.True(t, decision.GetAllowed())
	require.NotEmpty(t, decision.GetScopeNodeId())
	decision, err = svc.CheckDelegatedRecordAccess(viewer, f.org, []string{admin, member}, "example.record", "record-a", "read", current)
	require.NoError(t, err)
	require.False(t, decision.GetAllowed(), "every delegated subject must read independently")

	// The collection projection behind ListReadableSourceCollections.
	readable := func(ctx context.Context, subjects []string, resources []string) []string {
		t.Helper()
		var ids []string
		require.NoError(t, testStore.WithSourceReadSnapshot(ctx, f.org, func(snapshot context.Context) error {
			rows, err := testStore.ListReadableSourcesPage(snapshot, f.org, subjects, resources, "", 10)
			for _, row := range rows {
				ids = append(ids, row.SourceId)
			}
			return err
		}))
		return ids
	}
	require.Equal(t, []string{source}, readable(viewer, []string{admin}, []string{"documents"}))
	require.Empty(t, readable(viewer, []string{admin, member}, []string{"documents"}))
	require.Empty(t, readable(auth.WithVerifiedDatabaseIdentity(testCtx, member, f.org), []string{member}, []string{"documents"}))
	require.Empty(t, readable(viewer, []string{admin}, nil),
		"platform authority replaces the grant, never the module's declaration of its content")
}
