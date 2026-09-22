//go:build !pure

package business_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The rule itself, away from the database: current is how many eligible
// administrators the organization has, projected how many would remain.
func TestOrgAdminContinuityRule(t *testing.T) {
	for _, tc := range []struct {
		name               string
		current, projected int
		reject             bool
	}{
		{"the sole administrator would go", 1, 0, true},
		{"one of two would go", 2, 1, false},
		{"nothing changes", 1, 1, false},
		{"an administrator is added", 1, 2, false},
		// An organization that already has none stays repairable — otherwise
		// even an operator adding an administrator back would be refused.
		{"already none", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := business.OrgAdminContinuity(tc.current, tc.projected)
			if tc.reject {
				require.ErrorIs(t, err, business.ErrOrgAdminContinuity)
				return
			}
			require.NoError(t, err)
		})
	}
}

// A role that keeps the target administrative can never reduce the count, so
// the guard settles it from the argument alone and never takes the lock.
func TestIsOrgAdminRole(t *testing.T) {
	require.True(t, business.IsOrgAdminRole("owner"))
	require.True(t, business.IsOrgAdminRole("admin"))
	require.False(t, business.IsOrgAdminRole("member"))
	require.False(t, business.IsOrgAdminRole(""))
}

// auditBarrierStore holds both contenders at the decision boundary until each
// has arrived, so the interleaving is forced rather than waited for.
//
// The barrier sits immediately before the administration lock, not on the
// roster read: the read now happens under that lock, so blocking inside it
// would park the winner behind a contender that cannot reach the barrier until
// the winner commits. That deadlock is the guarantee under test, which is
// exactly why the barrier belongs one step earlier — both transactions are open
// and about to decide, and nothing about the ordering after that is arranged.
type auditBarrierStore struct {
	business.Store
	mu        sync.Mutex
	remaining int
	barrier   sync.WaitGroup
}

// Releasing at most `remaining` times keeps an extra guard call — a retry, or a
// future mutation that checks twice — from driving the WaitGroup negative. A
// miscounted barrier should fail the assertion it was set up for, not panic the
// suite out from under every other test.
func (s *auditBarrierStore) LockOrgAdministration(ctx context.Context, orgID string) error {
	s.mu.Lock()
	if s.remaining > 0 {
		s.remaining--
		s.barrier.Done()
	}
	s.mu.Unlock()
	s.barrier.Wait()
	return s.Store.LockOrgAdministration(ctx, orgID)
}

// sequencedStore runs a hook after the guard has counted administrators but
// before it decides, so a contender can commit underneath a known-stale count.
type sequencedStore struct {
	business.Store
	afterCount func()
}

func (s *sequencedStore) CountOrgAdministrators(ctx context.Context, orgID string, excludeUserID string) (int, int, error) {
	total, others, err := s.Store.CountOrgAdministrators(ctx, orgID, excludeUserID)
	if s.afterCount != nil {
		s.afterCount()
	}
	return total, others, err
}

// setUserStatus writes a user status directly. DeleteUser is a soft delete, so
// this is the same state a deleted or suspended identity is left in.
func setUserStatus(t *testing.T, userID string, status string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `UPDATE users SET status = $2 WHERE uuid = $1`, userID, status)
		return err
	}))
}

func continuityService(t *testing.T, store business.Store) *business.Service {
	t.Helper()
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetEntitlementChecker(business.NewDefaultEntitlementChecker(store))
	return service
}

// barrieredService returns a service whose membership mutations all pass one
// two-party barrier at the decision boundary.
func barrieredService(t *testing.T, contenders int) *business.Service {
	t.Helper()
	store := &auditBarrierStore{Store: testStore, remaining: contenders}
	store.barrier.Add(contenders)
	return continuityService(t, store)
}

func orgRosterRoles(t *testing.T, ctx context.Context, orgID string) map[string]gen.OrgRole {
	t.Helper()
	var members []*gen.OrgMembership
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		members, err = testStore.ListOrgMembers(ctx, orgID)
		return err
	}))
	roles := make(map[string]gen.OrgRole, len(members))
	for _, m := range members {
		roles[m.UserId] = m.Role
	}
	return roles
}

// requireSurvivingAdministrator asserts the invariant against what is actually
// persisted, not against what the calls returned.
func requireSurvivingAdministrator(t *testing.T, ctx context.Context, orgID string) map[string]gen.OrgRole {
	t.Helper()
	roles := orgRosterRoles(t, ctx, orgID)
	admins := 0
	for _, role := range roles {
		if role == gen.OrgRole_ORG_ROLE_OWNER || role == gen.OrgRole_ORG_ROLE_ADMIN {
			admins++
		}
	}
	require.GreaterOrEqual(t, admins, 1,
		"the organization committed to a state with no administrative membership: %v", roles)
	return roles
}

func countErrors(errs ...error) (succeeded int, rejected int) {
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		rejected++
	}
	return succeeded, rejected
}

// Audit probe A01-1, adapted to current main. AddOrgMember is an upsert, so it
// is the demotion path, and its seat check deliberately admits an update on a
// full organization — nothing else was applying the last-administrator rule to
// it.
func TestAuditProbeRejectLastOwnerDemotion(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")

	err := testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: owner, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	})
	require.ErrorIs(t, err, business.ErrOrgAdminContinuity,
		"upserting membership must not demote the last administrator")

	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER, orgRosterRoles(t, testCtx, org)[owner],
		"the rejected demotion must not have been applied")
}

// Audit probe A01-2, adapted to current main. Both transactions read a
// two-administrator organization and each removes one; without a shared
// organization-scoped lock both commit and the organization is left with none.
func TestAuditProbeConcurrentLastAdminRemoval(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	other, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: other, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))

	service := barrieredService(t, 2)
	results := make(chan error, 2)
	for _, target := range []string{owner, other} {
		go func(target string) {
			results <- service.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
				OrgId: org, UserId: target,
			})
		}(target)
	}
	succeeded, rejected := countErrors(<-results, <-results)

	require.Equal(t, 1, succeeded, "exactly one concurrent removal may commit")
	require.Equal(t, 1, rejected, "the contender that would empty the organization must be rejected")
	requireSurvivingAdministrator(t, testCtx, org)
}

// demotion against demotion: the same rule, reached through AddOrgMember from
// both sides.
func TestConcurrentDemotionsKeepAnAdministrator(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	other, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: other, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))

	service := barrieredService(t, 2)
	results := make(chan error, 2)
	for _, target := range []string{owner, other} {
		go func(target string) {
			results <- service.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
				OrgId: org, UserId: target, Role: gen.OrgRole_ORG_ROLE_MEMBER,
			})
		}(target)
	}
	succeeded, rejected := countErrors(<-results, <-results)

	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, rejected)
	roles := requireSurvivingAdministrator(t, testCtx, org)
	require.Len(t, roles, 2, "a rejected demotion must not remove anyone")
}

// demotion against removal: the AddMember upsert cannot slip past a rule
// RemoveMember enforces, in either order.
func TestConcurrentDemotionAndRemovalKeepAnAdministrator(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	other, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: other, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))

	service := barrieredService(t, 2)
	results := make(chan error, 2)
	go func() {
		results <- service.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
			OrgId: org, UserId: other, Role: gen.OrgRole_ORG_ROLE_MEMBER,
		})
	}()
	go func() {
		results <- service.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
			OrgId: org, UserId: owner,
		})
	}()
	succeeded, rejected := countErrors(<-results, <-results)

	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, rejected)
	requireSurvivingAdministrator(t, testCtx, org)
}

// Ownership transfer is promotion followed by the old owner's departure, so the
// interesting race is the promotion against that departure.
//
// A promotion can only raise the administrator count, so it settles from its
// argument and never takes the administration lock — which makes this
// interleaving reachable rather than hypothetical: the removal counts one
// administrator, the promotion commits a second underneath it, and the removal
// then decides. It must decide on what it counted and refuse, leaving both
// administrators standing, rather than act on a count it no longer believes.
func TestOwnerTransferRacingRemovalKeepsAnAdministrator(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	successor, _ := mustUserAndOrg(t, testCtx, "successor@example.com", "example-successor", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: successor, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))

	var promotion error
	store := &sequencedStore{Store: testStore, afterCount: func() {
		promotion = testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
			OrgId: org, UserId: successor, Role: gen.OrgRole_ORG_ROLE_OWNER,
		})
	}}
	removal := continuityService(t, store).RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: org, UserId: owner,
	})

	require.NoError(t, promotion, "a promotion must not queue behind an in-flight removal")
	require.ErrorIs(t, removal, business.ErrOrgAdminContinuity)
	roles := requireSurvivingAdministrator(t, testCtx, org)
	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER, roles[owner])
	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER, roles[successor])
}

// One administrator plus ordinary members: churn among the members is
// unaffected by a rule about administrators.
func TestOrdinaryMemberChurnIsUnaffected(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	first, _ := mustUserAndOrg(t, testCtx, "first@example.com", "example-first", "ExampleCorp")
	second, _ := mustUserAndOrg(t, testCtx, "second@example.com", "example-second", "Placeholder Org")

	for _, member := range []string{first, second} {
		require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
			OrgId: org, UserId: member, Role: gen.OrgRole_ORG_ROLE_MEMBER,
		}))
	}
	for _, member := range []string{first, second} {
		require.NoError(t, testService.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
			OrgId: org, UserId: member,
		}))
	}

	roles := requireSurvivingAdministrator(t, testCtx, org)
	require.Len(t, roles, 1)
	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER, roles[owner])
}

// A change already applied is not a violation. Re-submitting the sole owner's
// existing role is a no-op that must stay admissible, or a retried request
// would fail where the original succeeded.
func TestAlreadyAppliedChangeStaysAdmissible(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	other, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "ExampleCorp")

	for range 3 {
		require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
			OrgId: org, UserId: owner, Role: gen.OrgRole_ORG_ROLE_OWNER,
		}))
	}
	// Removing someone who is not a member changes nothing and must not be
	// rejected as though it emptied the organization.
	require.NoError(t, testService.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: org, UserId: other,
	}))

	roles := requireSurvivingAdministrator(t, testCtx, org)
	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER, roles[owner])
}

// A refused demotion is a precondition failure, never a seat-quota one — even
// when the organization is in fact at its seat limit, which is the state most
// likely to produce the wrong diagnosis.
func TestContinuityRejectionIsNotReportedAsAQuotaFailure(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	setEntitlementLimit(t, testCtx, org, owner, business.EntitlementSeats, 1)

	err := testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: owner, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	})
	require.ErrorIs(t, err, business.ErrOrgAdminContinuity)
	require.NotErrorIs(t, err, business.ErrEntitlementQuotaExceeded,
		"an invariant violation must not be misreported as an exhausted quota")
}

// Fixture convergence seeds bootstrap state and bypasses seat admission, but a
// fixture that would demote an organization's last administrator should fail
// the boot rather than converge an organization nobody can administer.
func TestFixtureConvergenceCannotDemoteTheLastAdministrator(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")

	require.ErrorIs(t, testService.ConvergeFixtureOrgMember(testCtx, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: owner, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}), business.ErrOrgAdminContinuity)

	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER, orgRosterRoles(t, testCtx, org)[owner])
}

// The owner of record is provenance, not authority: it is written once at
// creation and no authorization decision derives from it. Demoting that user
// therefore strips their authority immediately and leaves owner_id alone, and
// the two are allowed to disagree rather than being silently reconciled.
func TestOwnerOfRecordIsProvenanceNotAuthority(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	successor, _ := mustUserAndOrg(t, testCtx, "successor@example.com", "example-successor", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: successor, Role: gen.OrgRole_ORG_ROLE_OWNER,
	}))

	// With a second owner in place the owner of record may be demoted.
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: owner, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))

	var record *gen.Organization
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		var err error
		record, err = testStore.GetOrganization(ctx, org)
		return err
	}))
	require.Equal(t, owner, record.OwnerId,
		"owner_id records who created the organization; a role change does not make that untrue")

	roles := requireSurvivingAdministrator(t, testCtx, org)
	require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER, roles[owner],
		"the owner of record holds no administrative authority once demoted")

	// Ownership transfer is therefore not a precondition for the owner of
	// record leaving: the successor's membership is what carries authority.
	require.NoError(t, testService.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: org, UserId: owner,
	}))
	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER,
		requireSurvivingAdministrator(t, testCtx, org)[successor])
}

// Deleting a whole organization is not a membership mutation and never reaches
// the guard: the memberships go with the organization row by cascade. The
// invariant constrains who may be removed from a live organization, not whether
// an organization may cease to exist.
func TestWholeOrganizationDeletionIsNotBlocked(t *testing.T) {
	clearData(t)
	_, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	require.Len(t, orgRosterRoles(t, testCtx, org), 1)

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org)
		return err
	}), "a sole-administrator organization must still be deletable as a whole")

	var remaining int
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM organization_members WHERE org_id = $1`, org).Scan(&remaining)
	}))
	require.Zero(t, remaining, "the administrator's membership must go with the organization")
}

// An administrator who cannot sign in administers nothing: findIdentity refuses
// any identity that is not active, and DeleteUser is a soft delete that leaves
// the membership row standing. Counting those memberships would let the last
// usable administrator be removed while the invariant reported the organization
// healthy.
func TestIneligibleAdministratorDoesNotSatisfyTheInvariant(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	ghost, _ := mustUserAndOrg(t, testCtx, "ghost@example.com", "example-ghost", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: ghost, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))
	setUserStatus(t, ghost, "deleted")

	require.ErrorIs(t, testService.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: org, UserId: owner,
	}), business.ErrOrgAdminContinuity,
		"the last administrator who can still sign in must not be removable")

	require.ErrorIs(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: owner, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}), business.ErrOrgAdminContinuity,
		"nor demotable")

	// Restoring the ghost makes the organization administrable again, so the
	// rule tracks eligibility rather than freezing on a stale verdict.
	setUserStatus(t, ghost, "active")
	require.NoError(t, testService.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: org, UserId: owner,
	}))
}

// The pre-authentication resolver upserts membership itself, outside the
// business layer, so the invariant has to hold on that path too. Redeeming a
// member-role invitation addressed to the only administrator is a demotion.
func TestResolverInviteRedemptionCannotDemoteTheLastAdministrator(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	token := seedPendingInvitation(t, org, owner, "owner@example.com", "member",
		time.Now().Add(24*time.Hour))

	_, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "example-owner",
		ProviderEmail: "owner@example.com",
		Profile:       map[string]string{"invitation_token": token},
	})
	require.ErrorIs(t, err, business.ErrOrgAdminContinuity)
	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER, orgRosterRoles(t, testCtx, org)[owner],
		"a rejected redemption must not have demoted the administrator")
}
