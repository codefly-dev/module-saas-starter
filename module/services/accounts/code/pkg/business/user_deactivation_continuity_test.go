package business_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func deleteUser(service *business.Service, access business.Identity, userID string) error {
	return service.DeleteUser(testCtx, userID, access,
		&gen.GetUserRequest{Identifier: &gen.GetUserRequest_Uuid{Uuid: userID}})
}

// mustUser registers an identity that belongs to no organization. Every fixture
// here turns on exactly which administrations an identity holds, and
// mustUserAndOrg makes its user the sole administrator of an organization of its
// own — which would make almost every deletion below refused for a reason the
// test is not about.
func mustUser(t *testing.T, email, providerID string) string {
	t.Helper()
	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: email,
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: providerID, ProviderEmail: email,
		},
	})
	require.NoError(t, err)
	return registered.User.Uuid
}

// mustOrg creates a further organization owned by an existing identity.
func mustOrg(t *testing.T, ownerID, slug, name string) string {
	t.Helper()
	created, err := testService.CreateOrganization(testCtx, ownerID,
		&gen.CreateOrganizationRequest{Name: name, Slug: slug})
	require.NoError(t, err)
	return created.Organization.Id
}

// userStatus reads the column directly: GetUser hides a soft-deleted row, which
// is the state most of these assertions are about.
func userStatus(t *testing.T, userID string) string {
	t.Helper()
	var status string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		return tx.QueryRow(ctx, `SELECT status FROM users WHERE uuid = $1`, userID).Scan(&status)
	}))
	return status
}

// mustMember registers an identity and puts it in an organization as an
// ordinary member. Somebody other than the administrator has to be in the
// organization for its administration to be worth preserving — an organization
// nobody else is in is left empty by a deactivation, not unadministrable.
func mustMember(t *testing.T, actorID, orgID, email, providerID string) string {
	t.Helper()
	member := mustUser(t, email, providerID)
	require.NoError(t, testService.AddOrgMember(testCtx, actorID, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: member, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	return member
}

// Deleting the only administrator leaves the organization administrable by
// nobody, and nothing on main refused it: the membership row survives a soft
// delete while the identity stops being able to authenticate at all.
func TestDeletingTheSoleAdministratorIsRefused(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	mustMember(t, owner, org, "member@example.com", "example-member")

	err := deleteUser(testService, business.Identity{UserID: owner}, owner)
	require.ErrorIs(t, err, business.ErrIdentityAdminContinuity)

	require.Equal(t, "active", userStatus(t, owner),
		"a refused deactivation must not have been applied")
	requireSurvivingAdministrator(t, testCtx, org)
}

// The refusal is actionable or it is noise: the caller has to be told which
// organizations are waiting on an administrator handover, including when one
// identity administers several.
func TestDeletionRefusalNamesEveryStrandedOrganization(t *testing.T) {
	clearData(t)
	owner, first := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	second := mustOrg(t, owner, "example-owner-second", "ExampleCorp")
	mustMember(t, owner, first, "first-member@example.com", "example-first-member")
	mustMember(t, owner, second, "second-member@example.com", "example-second-member")
	// A third organization the same identity co-administers. It keeps an
	// administrator either way, so it must not be named — and neither must the
	// personal organization RegisterUser gave this identity, which nobody else
	// is in.
	peer, third := mustUserAndOrg(t, testCtx, "peer@example.com", "example-peer", "Placeholder Org")
	require.NoError(t, testService.AddOrgMember(testCtx, peer, &gen.AddOrgMemberRequest{
		OrgId: third, UserId: owner, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))

	err := deleteUser(testService, business.System(), owner)

	var continuity *business.IdentityAdminContinuityError
	require.ErrorAs(t, err, &continuity)
	require.ElementsMatch(t, []string{first, second}, continuity.Organizations,
		"every organization left without an administrator, and only those")
}

// The personal organization RegisterUser creates is solely owned by its
// identity and has nobody else in it, so deactivating that identity leaves it
// empty rather than unadministrable. Without this the rule would refuse every
// deletion on the platform, which is how it would go unnoticed: it would look
// like a very strict invariant rather than a broken one.
func TestDeletingAnIdentityNobodySharesAnOrganizationWithIsAllowed(t *testing.T) {
	clearData(t)
	loner := mustUser(t, "loner@example.com", "example-loner")

	require.NoError(t, deleteUser(testService, business.Identity{UserID: loner}, loner))
	require.Equal(t, "deleted", userStatus(t, loner))
}

// The rule is about administrative continuity, not about deletion, so an
// identity nobody depends on administratively still goes.
func TestDeletingAnIdentityWithASuccessorIsAllowed(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	mustMember(t, owner, org, "member@example.com", "example-member")
	successor := mustUser(t, "successor@example.com", "example-successor")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: successor, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))

	require.NoError(t, deleteUser(testService, business.Identity{UserID: owner}, owner))
	require.Equal(t, "deleted", userStatus(t, owner))
	requireSurvivingAdministrator(t, testCtx, org)
}

// An ordinary member's departure was never an administrative question, and the
// guard must not turn every deletion into one.
func TestDeletingAnOrdinaryMemberIsUnaffected(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	member := mustMember(t, owner, org, "member@example.com", "example-member")

	require.NoError(t, deleteUser(testService, business.System(), member))
	require.Equal(t, "deleted", userStatus(t, member))
}

// The self-service path resolves the organizations through a user-scoped
// transaction, which reads neither organization_members nor a co-member's users
// row on its own. Getting that wrong reads as "administers nothing" and admits
// the deletion, so the two entry points are worth separating: this asserts the
// tenant-scoped resolution specifically, where the platform-admin cases above
// span tenants.
func TestSelfDeletionResolvesAdministrationThroughTheTenantPath(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	mustMember(t, owner, org, "member@example.com", "example-member")

	require.ErrorIs(t, deleteUser(testService, business.Identity{UserID: owner}, owner),
		business.ErrIdentityAdminContinuity,
		"a user-scoped transaction must still see the organization it administers")

	successor := mustUser(t, "successor@example.com", "example-successor")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: successor, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))
	require.NoError(t, deleteUser(testService, business.Identity{UserID: owner}, owner))
}

// An administrator who is already deactivated is not one, so the identity that
// is still active is the last: counting the membership row alone would let both
// go and report the organization healthy throughout.
func TestAlreadyDeactivatedAdministratorsDoNotStandIn(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	mustMember(t, owner, org, "member@example.com", "example-member")
	ghost := mustUser(t, "ghost@example.com", "example-ghost")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: ghost, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))
	setUserStatus(t, ghost, "suspended")

	require.ErrorIs(t, deleteUser(testService, business.System(), owner),
		business.ErrIdentityAdminContinuity)

	setUserStatus(t, ghost, "active")
	require.NoError(t, deleteUser(testService, business.System(), owner))
}

// An identity that is already deactivated administers nothing, so deleting it
// takes nothing away and is not refused — including when its organization is
// one nobody can administer. The membership rows stay standing for an operator
// to repair, which is the state the membership integrity diagnostic reports.
func TestDeletingAnAlreadyDeactivatedAdministratorIsAllowed(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	mustMember(t, owner, org, "member@example.com", "example-member")
	setUserStatus(t, owner, "suspended")

	require.NoError(t, deleteUser(testService, business.System(), owner))
	require.Equal(t, "deleted", userStatus(t, owner))
	require.Len(t, orgRosterRoles(t, testCtx, org), 2,
		"the memberships stay for an operator to repair")
}

// The race the guard's locks exist for. Both transactions see two eligible
// administrators; one removes an administrative membership and the other
// deactivates the identity behind the other membership. Without a shared
// organization-scoped lock across the status write, both commit.
func TestConcurrentDeactivationAndRemovalKeepAnAdministrator(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	mustMember(t, owner, org, "member@example.com", "example-member")
	other := mustUser(t, "admin@example.com", "example-admin")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: other, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))

	service := barrieredService(t, 2)
	results := make(chan error, 2)
	go func() {
		results <- service.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
			OrgId: org, UserId: owner,
		})
	}()
	go func() {
		results <- deleteUser(service, business.System(), other)
	}()
	succeeded, rejected := countErrors(<-results, <-results)

	require.Equal(t, 1, succeeded, "exactly one of the two may commit")
	require.Equal(t, 1, rejected, "the contender that would strand the organization must be rejected")
	requireEligibleAdministrator(t, org)
}

// Two identities administering the same two organizations, deactivated at once:
// the locks are taken in one order by both, so they queue instead of deadlock,
// and whichever loses is the one that would have stranded the organizations.
func TestConcurrentDeactivationsOverSharedOrganizationsDoNotDeadlock(t *testing.T) {
	clearData(t)
	first, left := mustUserAndOrg(t, testCtx, "first@example.com", "example-first", "Acme")
	second, right := mustUserAndOrg(t, testCtx, "second@example.com", "example-second", "ExampleCorp")
	mustMember(t, first, left, "left-member@example.com", "example-left-member")
	mustMember(t, second, right, "right-member@example.com", "example-right-member")
	require.NoError(t, testService.AddOrgMember(testCtx, first, &gen.AddOrgMemberRequest{
		OrgId: left, UserId: second, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))
	require.NoError(t, testService.AddOrgMember(testCtx, second, &gen.AddOrgMemberRequest{
		OrgId: right, UserId: first, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))

	service := barrieredService(t, 2)
	results := make(chan error, 2)
	for _, target := range []string{first, second} {
		go func(target string) {
			results <- deleteUser(service, business.System(), target)
		}(target)
	}
	succeeded, rejected := countErrors(<-results, <-results)

	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, rejected)
	requireEligibleAdministrator(t, left)
	requireEligibleAdministrator(t, right)
}

// Suspension is the one deactivation that is not refused: it is how a
// compromised account is contained, and an availability invariant must not keep
// a credential in somebody else's hands live. What it owes the operator is the
// consequence, on the record.
func TestSuspendingTheSoleAdministratorProceedsAndRecordsTheConsequence(t *testing.T) {
	clearData(t)
	admin, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "Acme")
	target, org := mustUserAndOrg(t, testCtx, "target@example.com", "example-target", "ExampleCorp")
	mustMember(t, target, org, "member@example.com", "example-member")
	require.NoError(t, testStore.GrantPlatformRole(testCtx, admin, "super_admin", admin))

	require.NoError(t, testService.SuspendUser(testCtx, admin, &gen.SuspendUserRequest{
		UserId: target, Reason: "credential compromise",
	}))

	require.Equal(t, "suspended", userStatus(t, target))

	var payload []byte
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		return tx.QueryRow(ctx, `
			SELECT payload FROM audit_events
			WHERE event_type = $1 AND resource_id = $2
			ORDER BY created_at DESC LIMIT 1`,
			string(business.EventUserSuspended), target).Scan(&payload)
	}))
	require.Contains(t, string(payload), "organizations_without_administrator")
	require.Contains(t, string(payload), org,
		"the audit record must name the organization the suspension left without an administrator")
}

// requireEligibleAdministrator asserts against what was persisted: at least one
// administrative membership held by an identity that can still authenticate.
func requireEligibleAdministrator(t *testing.T, orgID string) {
	t.Helper()
	var eligible int
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		return tx.QueryRow(ctx, `
			SELECT count(*)
			FROM organization_members AS member
			JOIN users AS holder ON holder.uuid = member.user_id
			WHERE member.org_id = $1
			  AND member.role IN ('owner', 'admin')
			  AND holder.status = 'active'`, orgID).Scan(&eligible)
	}))
	require.GreaterOrEqual(t, eligible, 1,
		"the organization committed to a state no active identity can administer")
}
