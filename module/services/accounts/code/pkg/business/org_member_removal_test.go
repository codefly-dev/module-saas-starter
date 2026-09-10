package business_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
)

// seedTeamMemberships creates named teams in org and puts userID in each with
// the given role, returning the team ids in order.
func seedTeamMemberships(t *testing.T, ctx context.Context, actorID, orgID, userID string, teams map[string]gen.TeamRole) []string {
	t.Helper()
	ids := make([]string, 0, len(teams))
	for name, role := range teams {
		created, err := testService.CreateTeam(ctx, actorID, &gen.CreateTeamRequest{OrgId: orgID, Name: name})
		require.NoError(t, err)
		require.NoError(t, testService.AddTeamMember(ctx, actorID, &gen.AddTeamMemberRequest{
			TeamId: created.Team.Id, UserId: userID, Role: role,
		}))
		ids = append(ids, created.Team.Id)
	}
	return ids
}

// teamMembershipCount counts the rows one user still holds across every team of
// one organization, read through the tenant-scoped path the application uses.
func teamMembershipCount(t *testing.T, ctx context.Context, orgID, userID string) int {
	t.Helper()
	var count int
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		teams, err := testStore.ListTeams(ctx, orgID)
		if err != nil {
			return err
		}
		for _, team := range teams {
			members, err := testStore.ListTeamMembers(ctx, team.Id)
			if err != nil {
				return err
			}
			for _, m := range members {
				if m.UserId == userID {
					count++
				}
			}
		}
		return nil
	}))
	return count
}

func isOrgMember(t *testing.T, ctx context.Context, orgID, userID string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		exists, err = testStore.OrgMemberExists(ctx, orgID, userID)
		return err
	}))
	return exists
}

func memberRemovedAuditCount(t *testing.T, ctx context.Context, orgID string) int64 {
	t.Helper()
	buckets, err := testService.AggregateAuditLog(ctx,
		business.AuditQuery{OrgID: orgID, EventType: string(business.EventOrgMemberRemoved)},
		business.AuditAggregationSpec{GroupBy: []string{"event_type"}})
	require.NoError(t, err)
	if len(buckets) == 0 {
		return 0
	}
	return buckets[0].Count
}

// Removing an organization member must take every team membership that member
// held in that organization with it, in the same commit. team_members is the
// one relation that still confers live permissions once the organization row is
// gone (permission resolution matches team-subject role assignments through it),
// so a survivor is retained authority, not cosmetic residue.
func TestRemoveOrgMemberDeletesEveryDependentTeamMembership(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner@a09-test.com", "a09-owner", "Placeholder Org")
	memberID, _ := mustUserAndOrg(t, ctx, "member@a09-test.com", "a09-member", "Placeholder Member Org")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))

	seedTeamMemberships(t, ctx, owner, orgID, memberID, map[string]gen.TeamRole{
		"platform":  gen.TeamRole_TEAM_ROLE_ADMIN,
		"support":   gen.TeamRole_TEAM_ROLE_MEMBER,
		"analytics": gen.TeamRole_TEAM_ROLE_OWNER,
	})
	require.Equal(t, 3, teamMembershipCount(t, ctx, orgID, memberID))

	require.NoError(t, testService.RemoveOrgMember(ctx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: orgID, UserId: memberID,
	}))

	require.False(t, isOrgMember(t, ctx, orgID, memberID))
	require.Zero(t, teamMembershipCount(t, ctx, orgID, memberID),
		"no team membership may outlive the organization membership it depends on")
}

// A member of several organizations who leaves one keeps everything in the
// others: the dependent-access delete is scoped by the organization the removal
// names, not by the user.
func TestRemoveOrgMemberPreservesOtherOrganizations(t *testing.T) {
	clearData(t)
	ctx := testCtx

	ownerA, orgA := mustUserAndOrg(t, ctx, "owner-a@a09-test.com", "a09-owner-a", "Placeholder Org A")
	ownerB, orgB := mustUserAndOrg(t, ctx, "owner-b@a09-test.com", "a09-owner-b", "Placeholder Org B")
	memberID, _ := mustUserAndOrg(t, ctx, "dual@a09-test.com", "a09-dual", "Placeholder Dual Org")

	for _, seat := range []struct {
		owner string
		org   string
	}{{ownerA, orgA}, {ownerB, orgB}} {
		require.NoError(t, testService.AddOrgMember(ctx, seat.owner, &gen.AddOrgMemberRequest{
			OrgId: seat.org, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
		}))
	}
	seedTeamMemberships(t, ctx, ownerA, orgA, memberID, map[string]gen.TeamRole{"a-platform": gen.TeamRole_TEAM_ROLE_ADMIN})
	seedTeamMemberships(t, ctx, ownerB, orgB, memberID, map[string]gen.TeamRole{"b-platform": gen.TeamRole_TEAM_ROLE_ADMIN})

	require.NoError(t, testService.RemoveOrgMember(ctx, ownerA, &gen.RemoveOrgMemberRequest{
		OrgId: orgA, UserId: memberID,
	}))

	require.Zero(t, teamMembershipCount(t, ctx, orgA, memberID))
	require.True(t, isOrgMember(t, ctx, orgB, memberID), "the other tenant's membership is untouched")
	require.Equal(t, 1, teamMembershipCount(t, ctx, orgB, memberID),
		"the other tenant's team access is untouched")
}

// Re-inviting a removed user restores the organization seat and nothing else.
// Team roles were deleted, not deactivated, so there is no row left to
// reactivate — the returning user comes back with no team privileges until
// somebody grants them again.
func TestRemoveThenReinviteDoesNotRestoreTeamPrivileges(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner-rejoin@a09-test.com", "a09-owner-rejoin", "Placeholder Rejoin Org")
	memberID, _ := mustUserAndOrg(t, ctx, "rejoin@a09-test.com", "a09-rejoin", "Placeholder Rejoin Member Org")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	teamIDs := seedTeamMemberships(t, ctx, owner, orgID, memberID, map[string]gen.TeamRole{
		"platform": gen.TeamRole_TEAM_ROLE_ADMIN,
	})

	require.NoError(t, testService.RemoveOrgMember(ctx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: orgID, UserId: memberID,
	}))
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))

	require.True(t, isOrgMember(t, ctx, orgID, memberID))
	require.Zero(t, teamMembershipCount(t, ctx, orgID, memberID),
		"a rejoining member must not silently regain the team roles they held before")

	// Name the specific team the member used to administer, so the assertion
	// is about that row rather than about an aggregate that happens to be zero.
	var roster []*gen.TeamMembership
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		roster, err = testStore.ListTeamMembers(ctx, teamIDs[0])
		return err
	}))
	for _, m := range roster {
		require.NotEqual(t, memberID, m.UserId,
			"the previous team-admin row must not be resurrected by rejoining")
	}
}

// cleanupFailingStore is the real store with the dependent-access delete
// replaced by a failure, so the removal's rollback can be observed against real
// PostgreSQL rather than asserted about.
type cleanupFailingStore struct {
	*infra.PostgresStore
	err error
}

func (s *cleanupFailingStore) RemoveOrgTeamMemberships(context.Context, string, string) (int64, error) {
	return 0, s.err
}

// A dependent-access cleanup that fails must abort the whole removal. The
// organization membership, every team membership, and the audit record all move
// together or not at all — reporting success while team authority survives is
// the exact failure this path exists to prevent.
func TestRemoveOrgMemberRollsBackWhenDependentCleanupFails(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner-rollback@a09-test.com", "a09-owner-rollback", "Placeholder Rollback Org")
	memberID, _ := mustUserAndOrg(t, ctx, "rollback@a09-test.com", "a09-rollback", "Placeholder Rollback Member Org")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	seedTeamMemberships(t, ctx, owner, orgID, memberID, map[string]gen.TeamRole{
		"platform": gen.TeamRole_TEAM_ROLE_ADMIN,
		"support":  gen.TeamRole_TEAM_ROLE_MEMBER,
	})
	auditBefore := memberRemovedAuditCount(t, ctx, orgID)

	injected := errors.New("injected dependent-access cleanup failure")
	failing := &cleanupFailingStore{PostgresStore: testStore, err: injected}
	svc, err := business.NewService(failing)
	require.NoError(t, err)
	emitter, err := business.NewDurableAuditEmitter(failing, failing)
	require.NoError(t, err)
	svc.SetAuditEmitter(emitter)

	err = svc.RemoveOrgMember(ctx, owner, &gen.RemoveOrgMemberRequest{OrgId: orgID, UserId: memberID})
	require.Error(t, err, "a failed dependent-access cleanup must not report success")

	require.True(t, isOrgMember(t, ctx, orgID, memberID),
		"the organization membership must survive a rolled-back removal")
	require.Equal(t, 2, teamMembershipCount(t, ctx, orgID, memberID),
		"every dependent team membership must survive a rolled-back removal")
	require.Equal(t, auditBefore, memberRemovedAuditCount(t, ctx, orgID),
		"no member-removed audit record may commit for a removal that rolled back")
}

// A team membership committed while the organization removal is in flight must
// not survive it. Ordering is pinned by a barrier rather than left to the
// scheduler: the team write commits first, then the removal runs, which is the
// interleaving the removal itself has to resolve.
//
// The other ordering — an insert that lands after the removal has committed —
// is rejected by the write-side organization-membership precondition owned by
// issue #530, not by this path, and is deliberately not asserted here.
func TestTeamMembershipCommittedBeforeOrgRemovalDoesNotSurvive(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner-race@a09-test.com", "a09-owner-race", "Placeholder Race Org")
	memberID, _ := mustUserAndOrg(t, ctx, "race@a09-test.com", "a09-race", "Placeholder Race Member Org")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	created, err := testService.CreateTeam(ctx, owner, &gen.CreateTeamRequest{OrgId: orgID, Name: "platform"})
	require.NoError(t, err)

	require.NoError(t, testService.AddTeamMember(ctx, owner, &gen.AddTeamMemberRequest{
		TeamId: created.Team.Id, UserId: memberID, Role: gen.TeamRole_TEAM_ROLE_ADMIN,
	}))
	require.Equal(t, 1, teamMembershipCount(t, ctx, orgID, memberID))

	require.NoError(t, testService.RemoveOrgMember(ctx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: orgID, UserId: memberID,
	}))
	require.Zero(t, teamMembershipCount(t, ctx, orgID, memberID))
}

// The (organization, user) advisory lock is what turns the interleaving above
// into the only reachable orderings, so it is tested as the exclusion primitive
// it is: two transactions naming the same pair cannot hold it at once.
//
// The contender announces itself before it blocks, and the holder then keeps
// the lock for a grace period and asserts the contender has not acquired it.
// The timing dependence only runs one way — a contender too slow to reach the
// lock within the grace period has not acquired it either, so the assertion
// cannot fail spuriously. The second assertion closes the other side: whenever
// the contender does acquire, the holder was already on its way out.
func TestLockOrgMembershipExcludesConcurrentTransactions(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgID := mustUserAndOrg(t, ctx, "owner-lock@a09-test.com", "a09-owner-lock", "Placeholder Lock Org")
	userID := business.NewIDString()

	var acquired, holderLeaving atomic.Bool
	var sawHolderLeave bool
	holding := make(chan struct{})
	attempting := make(chan struct{})

	var contenderErr error
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		<-holding
		close(attempting)
		contenderErr = testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			if err := testStore.LockOrgMembership(ctx, orgID, userID); err != nil {
				return err
			}
			acquired.Store(true)
			sawHolderLeave = holderLeaving.Load()
			return nil
		})
	}()

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := testStore.LockOrgMembership(ctx, orgID, userID); err != nil {
			return err
		}
		close(holding)
		<-attempting
		time.Sleep(2 * time.Second)
		if acquired.Load() {
			// Returned rather than asserted so this transaction still unwinds
			// and releases the lock; failing in place would strand the
			// contender and turn a clear failure into a timeout.
			holderLeaving.Store(true)
			return errors.New("a second transaction acquired the pair's lock while the first held it")
		}
		holderLeaving.Store(true)
		return nil
	}))
	done.Wait()

	require.NoError(t, contenderErr)
	require.True(t, acquired.Load(), "the contender must acquire the lock once it is released")
	require.True(t, sawHolderLeave, "the contender acquired before the holder finished")
}

// The lock is per (organization, user), so mutations of different members do
// not serialize against each other. A coarser key would make every team write
// in a busy organization queue behind every other.
func TestLockOrgMembershipDoesNotSerializeDistinctUsers(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgID := mustUserAndOrg(t, ctx, "owner-lock2@a09-test.com", "a09-owner-lock2", "Placeholder Lock Org Two")
	first, second := business.NewIDString(), business.NewIDString()

	holding := make(chan struct{})
	acquired := make(chan error, 1)
	go func() {
		<-holding
		acquired <- testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			return testStore.LockOrgMembership(ctx, orgID, second)
		})
	}()

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := testStore.LockOrgMembership(ctx, orgID, first); err != nil {
			return err
		}
		close(holding)
		// The other pair's lock must be taken and released while this
		// transaction is still open. The deadline turns a lock that wrongly
		// covers the whole organization into a failure rather than a hang.
		select {
		case err := <-acquired:
			return err
		case <-time.After(15 * time.Second):
			return errors.New("a distinct member's lock blocked behind this one")
		}
	}))
}

// LockOrgMembership is a transaction-scoped lock and says so rather than
// silently taking a session-scoped one that nothing would ever release.
func TestLockOrgMembershipRequiresATransaction(t *testing.T) {
	require.Error(t, testStore.LockOrgMembership(testCtx, business.NewIDString(), business.NewIDString()))
}

// failingInvalidator stands in for a cache that is unreachable at the moment a
// membership mutation commits.
type failingInvalidator struct {
	err   error
	calls int
}

func (f *failingInvalidator) InvalidateMembership(context.Context, string, string) error {
	f.calls++
	return f.err
}

// Cache invalidation runs after the removal has committed, so a cache that is
// down cannot be allowed to report the removal as failed — the member really is
// gone from the database. The failure is surfaced (logged through the single
// helper every membership mutation passes through) rather than swallowed, and
// the stale entry's own TTL bounds how long it can still answer.
func TestRemoveOrgMemberSurvivesCacheInvalidationFailure(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner-cache@a09-test.com", "a09-owner-cache", "Placeholder Cache Org")
	memberID, _ := mustUserAndOrg(t, ctx, "cache@a09-test.com", "a09-cache", "Placeholder Cache Member Org")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	seedTeamMemberships(t, ctx, owner, orgID, memberID, map[string]gen.TeamRole{
		"platform": gen.TeamRole_TEAM_ROLE_ADMIN,
	})

	invalidator := &failingInvalidator{err: errors.New("cache unreachable")}
	testService.SetMembershipInvalidator(invalidator)
	t.Cleanup(func() { testService.SetMembershipInvalidator(nil) })

	require.NoError(t, testService.RemoveOrgMember(ctx, owner, &gen.RemoveOrgMemberRequest{
		OrgId: orgID, UserId: memberID,
	}))

	require.Equal(t, 1, invalidator.calls, "invalidation must be attempted once, after the commit")
	require.False(t, isOrgMember(t, ctx, orgID, memberID))
	require.Zero(t, teamMembershipCount(t, ctx, orgID, memberID))
}
