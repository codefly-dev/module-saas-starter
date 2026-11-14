//go:build !pure

package infra_test

import (
	"context"
	"crypto/ed25519"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/storetest"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
)

// The authority questions a module asks in bursts — Check, Issue and
// ListResources — each read the issuer's live state through the core's
// sources, one store Read per source: the revision, the seal, every chain
// principal's epoch, the binding, then the decision's own read. Over this
// store each Read was a control-plane transaction of its own (a pool acquire,
// BEGIN, SET LOCAL ROLE, the queries, COMMIT and the session reset), so one
// question held and released the shared control-plane pool many times over,
// and a burst of them queued behind each other until the caller's deadline.
// WithAuthoritySnapshot answers every Read of one question from one
// transaction on a pool of its own.

type snapshotKeys struct{ public ed25519.PublicKey }

func (k snapshotKeys) PublicKeys(context.Context) (map[string]ed25519.PublicKey, error) {
	return map[string]ed25519.PublicKey{"k1": k.public}, nil
}

// snapshotScenario is a person holding a role on a solution, whose call
// reaches a store module through the solution's binding: the shape of a
// module verifying a person's evidence through another module.
type snapshotScenario struct {
	core                 *authority.Service
	org, admin, member   string
	solution, store      string
	kind                 string
	child                authority.Issued
	solutionInstallation string
}

func newSnapshotScenario(t *testing.T) *snapshotScenario {
	t.Helper()
	run := uuid.NewString()[:8]
	sc := &snapshotScenario{org: uuid.NewString(), admin: uuid.NewString(), member: uuid.NewString(),
		solution: "snapshot-solution-" + run, store: "snapshot-store-" + run, kind: "snapshot-" + run + ".item"}
	seedAuthorityIdentity(t, storetest.Identity{
		Organizations: []domain.Organization{{ID: sc.org, Name: "Snapshot " + run}},
		Members: []storetest.Member{
			{Organization: sc.org, Person: sc.admin, Administrator: true},
			{Organization: sc.org, Person: sc.member},
		},
	})
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	sc.core, err = authority.New(testStore.AuthorityCore(), authority.Config{Issuer: "https://snapshot.test", KeyID: "k1", Signer: private,
		Keys: snapshotKeys{public}, Replay: testStore.AuthorityCoreReplay(), HostInstallsModules: true})
	require.NoError(t, err)

	items := func(actions ...string) []*basev0.WorkScopeV1 {
		return []*basev0.WorkScopeV1{{ResourceKind: sc.kind, Actions: actions}}
	}
	note := []*basev0.WorkScopeV1{{ResourceKind: sc.solution + ".note", Actions: []string{"read", "write"}}}
	for _, m := range []domain.Module{
		{Name: sc.store, Principal: sc.store, Kind: domain.KindModule, Generation: 1, OwnerAware: true, ResourceKinds: items("read", "write")},
		{Name: sc.solution, Principal: sc.solution, Kind: domain.KindSolution, Generation: 1, OwnerAware: true, ResourceKinds: note,
			Bindings: map[string]domain.Binding{"store": {ID: "store", Audience: sc.store, InvokeScopes: items("read", "write")}},
			Roles: map[string][]*basev0.WorkScopeV1{
				domain.InstallerRole: append(append([]*basev0.WorkScopeV1{}, note...), items("read", "write")...),
				"viewer":             append(append([]*basev0.WorkScopeV1{}, note[0]), items("read")...),
			}},
	} {
		_, err := sc.core.ApplyModule(testCtx, m)
		require.NoError(t, err)
	}
	admin := authority.Person{ID: sc.admin, Organization: sc.org, SessionID: "session-" + sc.admin}
	approval, err := sc.core.Review(testCtx, admin, sc.org, sc.solution)
	require.NoError(t, err)
	approval.OperationID = uuid.NewString()
	inst, err := sc.core.Install(testCtx, admin, sc.org, sc.solution, domain.InstallerRole, admin.ID, &approval, nil)
	require.NoError(t, err)
	sc.solutionInstallation = inst.ID
	_, err = sc.core.GrantRole(testCtx, admin, authority.RoleRequest{Organization: sc.org, InstallationID: inst.ID, Role: "viewer", PersonID: sc.member})
	require.NoError(t, err)
	sc.child = sc.issue(t)
	return sc
}

func (sc *snapshotScenario) issue(t *testing.T) authority.Issued {
	t.Helper()
	member := authority.Person{ID: sc.member, Organization: sc.org, SessionID: "session-" + sc.member}
	root, err := sc.core.IssueForPerson(testCtx, member, authority.IssueRequest{Audience: sc.solution})
	require.NoError(t, err)
	child, err := sc.core.IssueChild(testCtx, root.Token, authority.Workload{Module: sc.solution}, authority.IssueRequest{Binding: "store"})
	require.NoError(t, err)
	return child
}

func (sc *snapshotScenario) list(ctx context.Context) ([]authority.Grant, error) {
	return sc.core.ListResources(ctx, sc.child.Token, sc.store, sc.kind, "read")
}

// controlPlaneTransactions is how many control-plane transactions this
// process has opened, every site summed.
func controlPlaneTransactions() int64 {
	var n int64
	for _, count := range infra.ControlPlaneCounters() {
		n += count
	}
	return n
}

func transactionsOf(t *testing.T, fn func() error) int64 {
	t.Helper()
	before := controlPlaneTransactions()
	require.NoError(t, fn())
	return controlPlaneTransactions() - before
}

// One authority question is one control-plane transaction under a snapshot,
// however many sources the core reads it through; a statement it asks again is
// answered from the snapshot rather than sent again; and it answers exactly
// what the same question answers read source by source.
func TestAnAuthorityQuestionReadsOneSnapshot(t *testing.T) {
	sc := newSnapshotScenario(t)
	member := authority.Person{ID: sc.member, Organization: sc.org, SessionID: "session-" + sc.member}
	root, err := sc.core.IssueForPerson(testCtx, member, authority.IssueRequest{Audience: sc.solution})
	require.NoError(t, err)

	var unsnapped, snapped []authority.Grant
	for _, question := range []struct {
		name string
		ask  func(ctx context.Context, snapshot bool) error
	}{
		{"ListResources", func(ctx context.Context, snapshot bool) (err error) {
			if snapshot {
				snapped, err = sc.list(ctx)
			} else {
				unsnapped, err = sc.list(ctx)
			}
			return err
		}},
		{"Check", func(ctx context.Context, _ bool) error {
			_, _, err := sc.core.Check(ctx, sc.child.Token, sc.store)
			return err
		}},
		{"Issue (child)", func(ctx context.Context, _ bool) error {
			_, err := sc.core.IssueChild(ctx, root.Token, authority.Workload{Module: sc.solution}, authority.IssueRequest{Binding: "store"})
			return err
		}},
	} {
		perSource := transactionsOf(t, func() error { return question.ask(testCtx, false) })
		var sent, replayed int
		once := transactionsOf(t, func() error {
			return testStore.WithAuthoritySnapshot(testCtx, func(ctx context.Context) error {
				err := question.ask(ctx, true)
				sent, replayed = infra.AuthoritySnapshotStatements(ctx)
				return err
			})
		})
		// Each transaction is BEGIN, SET LOCAL ROLE, COMMIT and the session
		// reset on release beside its statements.
		t.Logf("%s: %d transactions and %d statements (%d round trips) read source by source; 1 transaction and %d statements (%d round trips) under a snapshot",
			question.name, perSource, sent+replayed, int(perSource)*4+sent+replayed, sent, 4+sent)
		require.Greater(t, perSource, int64(3), "%s: the baseline this fixes, one transaction per source the core reads", question.name)
		require.Equal(t, int64(1), once, "%s: every Read of one question is answered from one transaction", question.name)
		require.Positive(t, replayed, "%s: a statement asked again within the question is not sent again", question.name)
	}
	require.NotEmpty(t, snapped)
	require.Equal(t, unsnapped, snapped, "the snapshot answers what the sources answer")
}

// A snapshot lives for one question and no longer: a revocation committed
// before a question starts is what that question reads, and nothing a
// snapshot read is kept for the next question.
func TestARevocationCommittedBeforeAQuestionIsWhatItReads(t *testing.T) {
	sc := newSnapshotScenario(t)
	ask := func() ([]authority.Grant, error) {
		var out []authority.Grant
		err := testStore.WithAuthoritySnapshot(testCtx, func(ctx context.Context) (err error) {
			out, err = sc.list(ctx)
			return err
		})
		return out, err
	}
	granted, err := ask()
	require.NoError(t, err)
	require.NotEmpty(t, granted)

	admin := authority.Person{ID: sc.admin, Organization: sc.org, SessionID: "session-" + sc.admin}
	_, err = sc.core.RevokeRole(testCtx, admin, authority.RoleRequest{Organization: sc.org, InstallationID: sc.solutionInstallation, Role: "viewer", PersonID: sc.member})
	require.NoError(t, err)
	_, err = ask()
	require.Error(t, err, "the very next question reads the revocation")
	_, err = sc.list(testCtx)
	require.Error(t, err, "and so does one read source by source")

	// The organization's authorization revision, the lever every RBAC change
	// accounts records moves, is read live the same way.
	sc = newSnapshotScenario(t)
	_, err = ask()
	require.NoError(t, err)
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.BumpAuthorizationRevision(sc.org) }))
	_, err = ask()
	require.ErrorContains(t, err, "revision")
}

// A snapshot opened before a revocation commits is not reused by a question
// that starts after it, even while the first is still open.
func TestAnOpenSnapshotIsNotSharedWithTheNextQuestion(t *testing.T) {
	sc := newSnapshotScenario(t)
	opened, revoked, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var inside, outer error
	go func() {
		defer close(finished)
		outer = testStore.WithAuthoritySnapshot(testCtx, func(ctx context.Context) error {
			_, inside = sc.list(ctx)
			close(opened)
			<-revoked
			return nil
		})
	}()
	// Released however the test ends, so a failed assertion never leaves the
	// first question holding its connection.
	defer func() { close(revoked); <-finished }()
	// A first question that fails before it is open never closes opened: it
	// ends the test with its reason rather than hanging it.
	select {
	case <-opened:
	case <-finished:
		t.Fatalf("the first question never opened its snapshot: %v", outer)
	}
	require.NoError(t, inside)
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.BumpAuthorizationRevision(sc.org) }))
	err := testStore.WithAuthoritySnapshot(testCtx, func(ctx context.Context) error { _, err := sc.list(ctx); return err })
	require.ErrorContains(t, err, "revision", "a question started after the revocation committed reads it")
}

// A write inside a snapshot ends it: what the question reads after its own
// write is read fresh, never the state from before it.
func TestAWriteInsideASnapshotIsReadBack(t *testing.T) {
	sc := newSnapshotScenario(t)
	read := func(ctx context.Context) (rev uint64) {
		require.NoError(t, testStore.AuthorityCore().Read(ctx, func(tx authority.Tx) (err error) {
			rev, err = tx.AuthorizationRevision(sc.org)
			return err
		}))
		return rev
	}
	require.NoError(t, testStore.WithAuthoritySnapshot(testCtx, func(ctx context.Context) error {
		before := read(ctx)
		servedWhileWriting := true
		require.NoError(t, testStore.AuthorityCore().Write(ctx, func(tx authority.Tx) error {
			servedWhileWriting = infra.AuthoritySnapshotServes(ctx, testStore)
			return tx.BumpAuthorizationRevision(sc.org)
		}))
		require.False(t, servedWhileWriting, "the snapshot ends when the write starts, not when it returns")
		require.Greater(t, read(ctx), before)
		return nil
	}))
}

// A failed statement aborts the snapshot's transaction. The question is
// refused for the read that failed, never for the abort a later statement
// meets, and a later Read reads fresh, as it did source by source.
func TestAFailedReadIsTheReasonItsQuestionGives(t *testing.T) {
	sc := newSnapshotScenario(t)
	require.NoError(t, testStore.WithAuthoritySnapshot(testCtx, func(ctx context.Context) error {
		open, err := infra.AuthoritySnapshotQuery(ctx, "SELECT 1/0")
		require.True(t, open)
		require.ErrorContains(t, err, "division by zero")
		// A statement still asked on the aborted transaction names that read.
		_, err = infra.AuthoritySnapshotQuery(ctx, "SELECT 1")
		require.ErrorContains(t, err, "division by zero")
		require.NotContains(t, err.Error(), "current transaction is aborted")
		// A later Read of the question is answered as it was read source by source.
		require.False(t, infra.AuthoritySnapshotServes(ctx, testStore))
		granted, err := sc.list(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, granted)
		return nil
	}))
}

// The pools are taken snapshot first, control plane second: a snapshot asked
// for by a caller already holding a control-plane transaction is refused
// rather than waited for while that connection is held.
func TestASnapshotIsNotOpenedUnderAControlPlaneTransaction(t *testing.T) {
	ran := false
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.WithAuthoritySnapshot(ctx, func(context.Context) error { ran = true; return nil })
	})
	require.ErrorContains(t, err, "holding a control-plane transaction")
	require.False(t, ran)
}

// A burst of questions from one caller, as a directory import fans out: each
// passage verified is the hop's Check and child Issue and the callee's
// ListResources. Every one is answered; the log says how long the burst took
// read source by source and under snapshots. The pool is whatever the test
// database URL configures: pool_max_conns=4 is the deployed default on a
// four-CPU node, and the measurement in the change's description ran through
// a proxy adding 0.25 ms each way.
func TestABurstOfAuthorityQuestionsIsAnswered(t *testing.T) {
	sc := newSnapshotScenario(t)
	member := authority.Person{ID: sc.member, Organization: sc.org, SessionID: "session-" + sc.member}
	root, err := sc.core.IssueForPerson(testCtx, member, authority.IssueRequest{Audience: sc.solution})
	require.NoError(t, err)
	passage := []func(context.Context) error{
		func(ctx context.Context) error { _, _, err := sc.core.Check(ctx, root.Token, sc.solution); return err },
		func(ctx context.Context) error {
			_, err := sc.core.IssueChild(ctx, root.Token, authority.Workload{Module: sc.solution}, authority.IssueRequest{Binding: "store"})
			return err
		},
		func(ctx context.Context) error { _, err := sc.list(ctx); return err },
	}
	const burst = 64
	run := func(snapshot bool) (time.Duration, time.Duration) {
		latencies := make([]time.Duration, burst)
		errs := make([]error, burst)
		start := time.Now()
		var wg sync.WaitGroup
		for i := range burst {
			wg.Add(1)
			go func() {
				defer wg.Done()
				began := time.Now()
				ctx, cancel := context.WithTimeout(testCtx, 10*time.Second)
				defer cancel()
				for _, ask := range passage {
					if snapshot {
						errs[i] = testStore.WithAuthoritySnapshot(ctx, ask)
					} else {
						errs[i] = ask(ctx)
					}
					if errs[i] != nil {
						break
					}
				}
				latencies[i] = time.Since(began)
			}()
		}
		wg.Wait()
		total := time.Since(start)
		for _, err := range errs {
			require.NoError(t, err)
		}
		sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })
		return total, latencies[burst*99/100]
	}
	run(false) // warm the pools
	bySource, bySourceP99 := run(false)
	snapped, snappedP99 := run(true)
	t.Logf("%d concurrent passages (Check, child Issue, ListResources): %v (p99 %v) read source by source, %v (p99 %v) under snapshots",
		burst, bySource, bySourceP99, snapped, snappedP99)
}
