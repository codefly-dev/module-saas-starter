//go:build !pure

package business_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The policy log on the PRODUCTION PATH: real narrowings, through the real
// service, against real PostgreSQL.
//
// WHAT IS REAL HERE AND WHAT IS NOT, because the distinction is the whole value
// of this file. The thing under test is the HOST'S half of the protocol — that a
// real uninstall appends before it applies, that the receipt and the revocation
// are one transaction, and that a commit which fails after an append landed
// stops this host serving. Every part of that half is the production one:
// `Service.UninstallSolution`, `WithPolicyLoggedNarrowing`, the receipts in
// `policy_log_commits` through `infra.PostgresStore`, and `RequireServing`.
//
// The external log is the one collaborator that is a double, and it has to be:
// it is external by design, it lives in a warehouse, and no deployment of it
// exists (pkg/infra/warehouse_policy_log.go says why). A double for a
// collaborator is not a double for the thing under test — the mistake this PR
// has already paid for twice is faking the PolicyLogStore, which IS the thing
// under test, and that one is real postgres here.
//
// The commit failures below are not injected either. They are real database
// errors from real production statements given input they cannot accept, which
// is why the gap they leave is a gap the real store wrote.

// recordingPolicyLog stands in for the external warehouse log: it issues real
// monotonic sequences and remembers every entry, so a test can assert what the
// host offered it and in what order.
type recordingPolicyLog struct {
	mu       sync.Mutex
	entries  []*business.PolicyLogEntry
	records  []*business.PolicyLogRecord
	sequence uint64
	failWith error
}

func (l *recordingPolicyLog) Append(
	_ context.Context, entry *business.PolicyLogEntry,
) (*business.PolicyLogReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failWith != nil {
		return nil, l.failWith
	}
	l.sequence++
	receipt := &business.PolicyLogReceipt{
		Receipt:    "warehouse-receipt-" + entry.OperationID,
		Sequence:   l.sequence,
		AppendedAt: time.Now().UTC(),
	}
	copied := *entry
	l.entries = append(l.entries, &copied)
	l.records = append(l.records, &business.PolicyLogRecord{
		PolicyLogEntry: copied,
		Receipt:        receipt.Receipt,
		Sequence:       receipt.Sequence,
		LoggedAt:       receipt.AppendedAt,
	})
	return receipt, nil
}

func (l *recordingPolicyLog) Entries(
	_ context.Context, after uint64, _ int,
) ([]*business.PolicyLogRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failWith != nil {
		return nil, l.failWith
	}
	var out []*business.PolicyLogRecord
	for _, record := range l.records {
		if record.Sequence > after {
			out = append(out, record)
		}
	}
	return out, nil
}

// appended returns the entries offered since a mark, so a test reads only its
// own narrowings out of a log the whole package shares.
func (l *recordingPolicyLog) appended(from int) []*business.PolicyLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*business.PolicyLogEntry(nil), l.entries[from:]...)
}

func (l *recordingPolicyLog) mark() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// testPolicyLog is the log every business DB test's narrowings go through.
//
// Wired for the WHOLE package rather than per test, on purpose: once a narrowing
// requires a witness, a service without a log cannot uninstall, revoke a scope
// or remove a team member at all, so every test that performs one would be
// testing a host in a configuration production is not meant to have.
var testPolicyLog = &recordingPolicyLog{}

// wirePolicyLogForTests gives the package's service the protocol's two halves:
// the double for the external log, and the REAL postgres store for the local
// receipts and cursor.
func wirePolicyLogForTests(service *business.Service, store business.PolicyLogStore) {
	service.SetPolicyLog(testPolicyLog, store)
}

// reachTheLog runs a real reconciliation pass so `reached_at` is fresh.
//
// The serving gate refuses a host that has not reached the log inside the
// staleness window, and a host that has never reached it at all has a NULL
// `reached_at` — so without this a serving assertion would pass for the wrong
// reason, reporting the gap it was looking for as staleness.
func reachTheLog(t *testing.T, ctx context.Context) *business.PolicyLogServingState {
	t.Helper()
	state, err := testService.ReconcilePolicyLog(ctx)
	require.NoError(t, err)
	return state
}

// closeGap commits an operation whose narrowing never applied, so one test's
// deliberate gap does not keep the host shut for every test after it.
// ClearAll does not touch the policy log relations — a receipt is evidence an
// append happened and the schema makes neither deletable — so the cleanup is a
// commit, which is also what an operator closing a real gap does.
func closeGap(t *testing.T, ctx context.Context, operationID string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return testStore.CommitPolicyLogOperation(ctx, operationID, time.Now().UTC())
	}))
}

// closeEveryGap is the cleanup a test that deliberately opens a gap owes the
// tests after it.
//
// It runs even when the test FAILS before reaching its own close, which is the
// case that matters: a gap left behind keeps every later serving assertion
// refusing, and that refusal reads as the later test's own bug. Registered with
// t.Cleanup rather than written at the end of the body for exactly that reason.
func closeEveryGap(t *testing.T, ctx context.Context) {
	t.Helper()
	for _, gap := range uncommittedOperations(t, ctx) {
		closeGap(t, ctx, gap.OperationID)
	}
}

// requireCommitted asserts that one operation is NOT among the uncommitted,
// rather than that nothing is.
//
// `clearData` cannot reset the policy log relations — neither is deletable, by
// design, because a receipt is the evidence that an append happened — so a gap
// another test left behind is still in the table. Asserting emptiness would make
// this test report somebody else's leftover as its own failure; asserting about
// the operation it appended is the claim it can actually make.
func requireCommitted(t *testing.T, ctx context.Context, operationID string) {
	t.Helper()
	for _, gap := range uncommittedOperations(t, ctx) {
		require.NotEqual(t, operationID, gap.OperationID,
			"the narrowing committed, so its receipt must have committed in the same transaction")
	}
}

func uncommittedOperations(t *testing.T, ctx context.Context) []*business.PolicyLogGap {
	t.Helper()
	var gaps []*business.PolicyLogGap
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		out, err := testStore.UncommittedPolicyLogOperations(ctx)
		gaps = out
		return err
	}))
	return gaps
}

// A real uninstall, through the real service: it appends, and the revocation and
// the receipt's commit land together.
//
// This is the test the condition asks for. Nothing is stubbed on the host's
// side: the installation is composed by InstallSolution, revoked by
// UninstallSolution, and the receipt is read back out of PostgreSQL.
func TestARealUninstallAppendsAndThenCommitsItsReceipt(t *testing.T) {
	clearData(t)
	ctx := testCtx
	mark := testPolicyLog.mark()

	adminID, orgID := mustUserAndOrg(t, ctx, "pluninstall@example.com", "pluninstall", "PolicyLog Co")
	roleID := seedInstallableRole(t, ctx, orgID)
	targetID, _ := declarePresence(t, "pluninstall-solution")
	installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/pluninstall:1.0.0",
		TargetID:        targetID,
		RootScopeLabel:  "PolicyLog Solution",
		RoleID:          roleID,
		AllowedScopes:   []string{"doc"},
	})
	require.NoError(t, err)

	// An install is not a narrowing, so it must not have been offered to the
	// log. A protocol that appended on every write would make "was authority
	// reduced" unanswerable from the log.
	require.Empty(t, testPolicyLog.appended(mark),
		"installing is not a narrowing and must not reach the policy log")

	require.NoError(t, testService.UninstallSolution(ctx, adminID, orgID, installation.Id))

	offered := testPolicyLog.appended(mark)
	require.Len(t, offered, 1, "one uninstall is one appended entry")
	entry := offered[0]
	require.Equal(t, business.PolicyLogNarrowed, entry.Decision)
	require.Equal(t, business.PolicyLogInstallation, entry.SubjectKind)
	require.Equal(t, installation.Id, entry.SubjectID)
	require.Equal(t, adminID, entry.Actor)
	require.Equal(t, orgID, entry.Policy["org_id"])
	require.Equal(t, "revoked", entry.Policy["status"],
		"the entry carries the authority as it stands AFTER the operation")

	// The receipt committed, so this operation is no gap: the revocation and the
	// receipt were one transaction.
	requireCommitted(t, ctx, entry.OperationID)

	// And the narrowing really happened — the receipt is not the only thing
	// that landed.
	_, err = testStore.LiveModuleAuthority(ctx, business.ModulePrincipalID("pluninstall"), orgID, installation.Id)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
}

// A commit that fails AFTER its append landed stops this host serving, and keeps
// it stopped until the gap closes.
//
// The failure is real: RevokeScope's production statement is handed an
// organisation id PostgreSQL cannot cast to uuid, so the transaction carrying
// both the delete and the receipt's commit aborts — after the append has already
// been accepted by the log and recorded here. That is precisely the
// append-ok/commit-fail state, reached without reaching into the protocol.
func TestACommitFailingAfterTheAppendStopsThisHostServing(t *testing.T) {
	clearData(t)
	ctx := testCtx
	mark := testPolicyLog.mark()
	// The gate refuses on ANY gap, so a gap another test left in a relation
	// nothing truncates would make the refusal below unattributable. Closed
	// before, and closed after even if this test fails on the way.
	closeEveryGap(t, ctx)
	t.Cleanup(func() { closeEveryGap(t, ctx) })

	// Serving first, so the refusal below is attributable to the gap.
	reachTheLog(t, ctx)
	require.NoError(t, testService.RequireServing(ctx),
		"the host must be serving before the gap, or the assertion proves nothing")

	err := testService.RevokeScope(ctx, "policy-log-actor", &gen.RevokeScopeRequest{
		OrgId:       "not-a-uuid",
		SubjectId:   "11111111-1111-1111-1111-111111111111",
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath:   "root",
		RoleId:      "22222222-2222-2222-2222-222222222222",
	})
	require.Error(t, err, "the narrowing's transaction must have failed")

	offered := testPolicyLog.appended(mark)
	require.Len(t, offered, 1, "the append must have landed before the commit was attempted")
	operationID := offered[0].OperationID

	gaps := uncommittedOperations(t, ctx)
	require.Len(t, gaps, 1)
	require.Equal(t, operationID, gaps[0].OperationID,
		"the appended-but-unapplied operation is the one whose commit failed")

	// THE SERVING CONSEQUENCE. The log says a narrowing happened and this host
	// has not applied it, so it must not serve the wider authority it still
	// holds locally.
	servingErr := testService.RequireServing(ctx)
	require.Error(t, servingErr, "a host holding an unapplied logged narrowing must stop serving")
	require.Equal(t, codes.Unavailable, status.Code(servingErr),
		"an unreconciled host is UNABLE, not refusing the caller: a PermissionDenied here would tell "+
			"every caller their authority was revoked when nothing of theirs was")
	require.Contains(t, servingErr.Error(), "not applied here")

	ok, state, err := testService.MayServe(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	require.Len(t, state.Gaps, 1)
	require.False(t, state.Unreachable, "the log was reachable; the gap is the reason")

	// Still refused on a later request, not just the first: the gate is not a
	// one-shot and the cached answer does not expire into serving.
	require.Error(t, testService.RequireServing(ctx))

	// EVERY REPLICA, not only the one whose commit failed. A second host over
	// the same database — its own process state, its own gate cache, having
	// performed no narrowing itself — must refuse too, because the gap lives in
	// the shared control-plane relation rather than in the memory of the
	// process that opened it. A gap that closed only its own replica would
	// leave the others serving exactly the authority the log says was revoked.
	replica, err := business.NewService(testStore)
	require.NoError(t, err)
	replica.SetPolicyLog(testPolicyLog, testStore)
	replicaErr := replica.RequireServing(ctx)
	require.Error(t, replicaErr, "a replica that did not perform the narrowing must still stop serving")
	require.Equal(t, codes.Unavailable, status.Code(replicaErr))

	// Closing the gap resumes serving. Nothing else changed, so the gap is what
	// the gate was refusing on.
	closeGap(t, ctx, operationID)
	require.Empty(t, uncommittedOperations(t, ctx))
	require.NoError(t, testService.RequireServing(ctx),
		"the host must serve again once the gap is closed")
	require.NoError(t, replica.RequireServing(ctx),
		"the replica must serve again too: it read the gap from the shared relation, "+
			"so closing it there is what reopens every host")
}

// An append the log refuses narrows nothing, and leaves the host serving.
//
// The other half of the asymmetry: authority was not reduced and nobody was told
// it was, so there is nothing to fail closed about.
func TestANarrowingTheLogRefusesChangesNothing(t *testing.T) {
	clearData(t)
	ctx := testCtx
	mark := testPolicyLog.mark()

	adminID, orgID := mustUserAndOrg(t, ctx, "plrefuse@example.com", "plrefuse", "Refuse Co")
	roleID := seedInstallableRole(t, ctx, orgID)
	targetID, _ := declarePresence(t, "plrefuse-solution")
	installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/plrefuse:1.0.0",
		TargetID:        targetID,
		RootScopeLabel:  "Refuse Solution",
		RoleID:          roleID,
		AllowedScopes:   []string{"doc"},
	})
	require.NoError(t, err)

	testPolicyLog.mu.Lock()
	testPolicyLog.failWith = errPolicyLogDown
	testPolicyLog.mu.Unlock()
	defer func() {
		testPolicyLog.mu.Lock()
		testPolicyLog.failWith = nil
		testPolicyLog.mu.Unlock()
	}()

	err = testService.UninstallSolution(ctx, adminID, orgID, installation.Id)
	require.Error(t, err)
	require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)

	require.Empty(t, testPolicyLog.appended(mark))
	require.Empty(t, uncommittedOperations(t, ctx),
		"a refused append records no receipt, so there is no gap")

	// The installation is untouched: the narrowing did not happen.
	_, err = testStore.LiveModuleAuthority(ctx, business.ModulePrincipalID("plrefuse"), orgID, installation.Id)
	require.NoError(t, err, "a refused append must leave authority exactly as it was")
}

var errPolicyLogDown = &policyLogDownError{}

type policyLogDownError struct{}

func (*policyLogDownError) Error() string { return "warehouse unreachable" }

// A real team-membership removal and a real scope-grant revocation both append.
//
// Together with the uninstall above and the withdrawal test beside it, this is
// the set the condition names; they are one test because each is the same
// assertion about a different call site.
func TestTeamRemovalAndScopeRevocationAppendBeforeTheyApply(t *testing.T) {
	clearData(t)
	ctx := testCtx

	ownerID, orgID := mustUserAndOrg(t, ctx, "plteam@example.com", "plteam", "Team Co")
	memberID, _ := mustUserAndOrg(t, ctx, "plmember@example.com", "plmember", "Member Co")
	require.NoError(t, testService.AddOrgMember(ctx, ownerID, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	team, err := testService.CreateTeam(ctx, ownerID, &gen.CreateTeamRequest{OrgId: orgID, Name: "Operators"})
	require.NoError(t, err)
	require.NoError(t, testService.AddTeamMember(ctx, ownerID, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id, UserId: memberID, Role: gen.TeamRole_TEAM_ROLE_MEMBER,
	}))

	mark := testPolicyLog.mark()
	require.NoError(t, testService.RemoveTeamMember(ctx, ownerID, &gen.RemoveTeamMemberRequest{
		TeamId: team.Team.Id, UserId: memberID,
	}))
	offered := testPolicyLog.appended(mark)
	require.Len(t, offered, 1)
	require.Equal(t, business.PolicyLogTeamMembership, offered[0].SubjectKind)
	require.Equal(t, team.Team.Id+"/"+memberID, offered[0].SubjectID)
	require.Equal(t, business.PolicyLogNarrowed, offered[0].Decision)
	requireCommitted(t, ctx, offered[0].OperationID)

	roleID := seedInstallableRole(t, ctx, orgID)
	// A grant must name a REGISTERED node: the scope tree is the authority, so
	// granting on a path nothing registered is refused before the policy log is
	// reached, and the revocation below would have nothing to narrow.
	_, err = testService.RegisterScopeNode(ctx, ownerID, &gen.RegisterScopeNodeRequest{
		OrgId: orgID, ScopePath: "plsol", Kind: business.ScopeNodeKindSolution, Label: "PolicyLog Solution",
	})
	require.NoError(t, err)
	grant, err := testService.GrantScope(ctx, ownerID, &gen.GrantScopeRequest{
		OrgId:       orgID,
		SubjectId:   memberID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath:   "plsol",
		RoleId:      roleID,
	})
	require.NoError(t, err)

	mark = testPolicyLog.mark()
	require.NoError(t, testService.RevokeScope(ctx, ownerID, &gen.RevokeScopeRequest{
		OrgId:       orgID,
		SubjectId:   memberID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath:   grant.Grant.ScopePath,
		RoleId:      grant.Grant.RoleId,
	}))
	offered = testPolicyLog.appended(mark)
	require.Len(t, offered, 1)
	require.Equal(t, business.PolicyLogScopeGrant, offered[0].SubjectKind)
	require.True(t, strings.HasPrefix(offered[0].SubjectID, orgID+"/"),
		"a scope grant is named by the key the revocation is addressed with")
	requireCommitted(t, ctx, offered[0].OperationID)
}

// A read the log refuses does NOT advance the cursor, which is what makes the
// staleness window close and the host stop serving.
//
// The window itself is unit-tested against a fixed clock (policy_log_test.go);
// what only a real store can show is that a failed pass leaves `reached_at`
// exactly where the last successful one put it. A pass that advanced it on
// failure would keep a host that cannot read the log reporting itself current
// forever — the deleted UnavailablePolicyLog's permanent-outage twin, failing
// open instead of closed.
func TestAFailedReadDoesNotAdvanceTheCursorThatKeepsTheHostServing(t *testing.T) {
	clearData(t)
	ctx := testCtx

	closeEveryGap(t, ctx)
	reachTheLog(t, ctx)
	require.NoError(t, testService.RequireServing(ctx))

	var reachedBefore *time.Time
	var sequenceBefore uint64
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		sequenceBefore, reachedBefore, err = testStore.PolicyLogCursorState(ctx)
		return err
	}))
	require.NotNil(t, reachedBefore)

	testPolicyLog.mu.Lock()
	testPolicyLog.failWith = errPolicyLogDown
	testPolicyLog.mu.Unlock()
	defer func() {
		testPolicyLog.mu.Lock()
		testPolicyLog.failWith = nil
		testPolicyLog.mu.Unlock()
		reachTheLog(t, ctx)
	}()

	_, err := testService.ReconcilePolicyLog(ctx)
	require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)

	var reachedAfter *time.Time
	var sequenceAfter uint64
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		sequenceAfter, reachedAfter, err = testStore.PolicyLogCursorState(ctx)
		return err
	}))
	require.NotNil(t, reachedAfter)
	require.True(t, reachedBefore.Equal(*reachedAfter),
		"an unreachable log must not refresh the instant the staleness window is measured from")
	require.Equal(t, sequenceBefore, sequenceAfter,
		"an unreachable log must not advance the reconciled sequence past entries nobody read")
}

// A real withdrawal, through the real reconciler: closing the solution target
// appends before it closes, and commits its receipt in the same transaction.
//
// The target close is the narrowing with the widest blast radius of the four —
// it revokes EVERY active installation naming the target — and it is the only
// one that does not start from an RPC, so it is the one most easily left off
// the protocol. It is driven here the way delivery drives it: a present
// generation, then a tombstone, through the reconciler's own pass.
func TestARealWithdrawalAppendsBeforeItClosesTheTarget(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	present := declaredBinding(t, solutionID, 4)
	mount := &deliveredSet{}
	mount.put(t, present)
	reconciler := newTestReconciler(t, mount)
	require.NoError(t, reconciler.RunOnce(testCtx))
	opened := liveTarget(t, present.Binding)
	require.NotNil(t, opened)

	// Presenting is not a narrowing: opening a target starts a period an
	// organisation may consent to, and nothing's authority was reduced.
	mark := testPolicyLog.mark()
	require.Empty(t, testPolicyLog.appended(mark))

	mount.put(t, tombstoneOf(t, present, 5))
	require.NoError(t, reconciler.RunOnce(testCtx))

	offered := testPolicyLog.appended(mark)
	require.Len(t, offered, 1, "one withdrawal with a live target is one appended entry")
	entry := offered[0]
	require.Equal(t, business.PolicyLogNarrowed, entry.Decision)
	require.Equal(t, opened.ID, entry.SubjectID,
		"the subject is the target identity whose consent period ended")
	require.Equal(t, uint64(5), entry.EnvelopeRevision,
		"the entry names the generation the withdrawal was decided against")
	require.Equal(t, "closed", entry.Policy["status"])
	require.Equal(t, "revoked", entry.Policy["installations"])
	requireCommitted(t, testCtx, entry.OperationID)

	require.Nil(t, liveTarget(t, present.Binding), "the narrowing really applied")
}

// A withdrawal for a binding with NO live target appends nothing.
//
// There is no period to end, so there is no narrowing to witness — and an entry
// here would be a logged narrowing that narrowed nothing, a gap that nothing
// could ever close by re-running the operation.
func TestAWithdrawalWithNoLiveTargetAppendsNothing(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	present := declaredBinding(t, solutionID, 2)
	mount := &deliveredSet{}
	mount.put(t, present)
	reconciler := newTestReconciler(t, mount)
	require.NoError(t, reconciler.RunOnce(testCtx))

	mount.put(t, tombstoneOf(t, present, 3))
	require.NoError(t, reconciler.RunOnce(testCtx))
	require.Nil(t, liveTarget(t, present.Binding))

	// A second withdrawal at a higher generation: nothing is live any more.
	mark := testPolicyLog.mark()
	mount.put(t, tombstoneOf(t, present, 4))
	require.NoError(t, reconciler.RunOnce(testCtx))
	require.Empty(t, testPolicyLog.appended(mark),
		"a withdrawal with nothing live to close must not append an entry nothing could close")
}
