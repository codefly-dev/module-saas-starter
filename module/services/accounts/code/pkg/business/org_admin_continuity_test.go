package business_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The rule itself, away from the database. Every row is a proposed change:
// role is what the named user would hold afterwards, "" a removal.
func TestOrgAdminContinuityRule(t *testing.T) {
	soleOwner := map[string]string{"owner": "owner"}
	twoAdmins := map[string]string{"owner": "owner", "second": "admin"}
	adminAndMembers := map[string]string{"owner": "admin", "a": "member", "b": "member"}
	noAdmins := map[string]string{"a": "member", "b": "member"}

	for _, tc := range []struct {
		name   string
		roster map[string]string
		user   string
		role   string
		reject bool
	}{
		{"sole owner removed", soleOwner, "owner", "", true},
		{"sole owner demoted to member", soleOwner, "owner", "member", true},
		{"sole owner kept as owner", soleOwner, "owner", "owner", false},
		{"sole owner moved to admin", soleOwner, "owner", "admin", false},
		{"one of two admins removed", twoAdmins, "second", "", false},
		{"one of two admins demoted", twoAdmins, "second", "member", false},
		{"both-are-one: last admin of two after the other went", soleOwner, "owner", "member", true},
		{"ordinary member removed", adminAndMembers, "a", "", false},
		{"ordinary member promoted", adminAndMembers, "a", "admin", false},
		{"non-member added as member", adminAndMembers, "new", "member", false},
		{"sole admin removed", adminAndMembers, "owner", "", true},
		// An organization that never had an administrator is exempt, so
		// historical data stays repairable rather than frozen.
		{"member removed from an admin-less organization", noAdmins, "a", "", false},
		{"admin added to an admin-less organization", noAdmins, "a", "admin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := business.OrgAdminContinuity(tc.roster, tc.user, tc.role)
			if tc.reject {
				require.ErrorIs(t, err, business.ErrOrgAdminContinuity)
				return
			}
			require.NoError(t, err)
		})
	}
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
	barrier sync.WaitGroup
}

func (s *auditBarrierStore) LockOrgAdministration(ctx context.Context, orgID string) error {
	s.barrier.Done()
	s.barrier.Wait()
	return s.Store.LockOrgAdministration(ctx, orgID)
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
	store := &auditBarrierStore{Store: testStore}
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
// interesting race is the promotion against that departure. Either order is
// safe — the promotion first makes the removal admissible, the removal first
// makes it inadmissible — but neither may leave the organization unadministered.
func TestOwnerTransferRacingRemovalKeepsAnAdministrator(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	successor, _ := mustUserAndOrg(t, testCtx, "successor@example.com", "example-successor", "ExampleCorp")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: successor, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))

	service := barrieredService(t, 2)
	results := make(chan error, 2)
	go func() {
		results <- service.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
			OrgId: org, UserId: successor, Role: gen.OrgRole_ORG_ROLE_OWNER,
		})
	}()
	go func() {
		results <- service.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{
			OrgId: org, UserId: owner,
		})
	}()
	succeeded, _ := countErrors(<-results, <-results)

	require.GreaterOrEqual(t, succeeded, 1, "the promotion must not be blocked by the removal")
	requireSurvivingAdministrator(t, testCtx, org)
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

// Deleting a whole organization is not a membership mutation and does not pass
// through the guard: the memberships go with the organization row by cascade.
// The invariant constrains who may be removed from a live organization, not
// whether an organization may cease to exist.
func TestWholeOrganizationDeletionIsNotBlocked(t *testing.T) {
	clearData(t)
	_, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	require.Len(t, orgRosterRoles(t, testCtx, org), 1)

	require.NoError(t, testStore.ClearAll(testCtx),
		"a sole-administrator organization must still be deletable as a whole")
	require.Empty(t, orgRosterRoles(t, testCtx, org))
}

// Invitation redemption is a membership upsert whose role is authoritative and
// overwrites whatever the accepting user already holds, so an invitation
// addressed to the only administrator at a lower role is a demotion reached
// through a different verb. Nothing about CreateInvitation prevents addressing
// an existing member, so the rule has to hold at redemption.
func TestInvitationRedemptionCannotDemoteTheLastAdministrator(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")

	created, err := testService.CreateInvitation(testCtx, owner, &gen.CreateInvitationRequest{
		OrgId: org,
		Email: "owner@example.com",
		Role:  gen.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.NoError(t, err)

	// Acceptance authorizes on verified email equality, which is a separate
	// precondition; verify the address so the continuity rule is what the
	// request actually reaches.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `UPDATE users SET email_verified = true WHERE uuid = $1`, owner)
		return err
	}))

	_, err = testService.AcceptInvitation(testCtx, owner, &gen.AcceptInvitationRequest{
		Credential: &gen.AcceptInvitationRequest_InvitationId{
			InvitationId: created.GetInvitation().GetId(),
		},
	})
	require.ErrorIs(t, err, business.ErrOrgAdminContinuity)
	require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER, orgRosterRoles(t, testCtx, org)[owner],
		"a rejected redemption must not have demoted the administrator")
}
