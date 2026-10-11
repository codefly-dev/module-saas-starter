//go:build !pure

package business_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// Pause only after the real first uninstall has deleted its standing grant and
// acquired the organization's revision row. The second uninstall reports its
// backend before entering the real store method, so the test can identify the
// exact waiter rather than an unrelated lock in the database.
type installationBatchBarrierStore struct {
	*infra.PostgresStore
	firstID       string
	firstDone     chan uint32
	resume        chan struct{}
	secondStarted chan uint32
	secondOnce    sync.Once
}

func (s *installationBatchBarrierStore) UninstallSolution(ctx context.Context, orgID, installationID string) (*gen.Installation, bool, error) {
	pid := storetx.Tx(ctx).Conn().PgConn().PID()
	if installationID != s.firstID {
		s.secondOnce.Do(func() { s.secondStarted <- pid })
	}
	installation, transitioned, err := s.PostgresStore.UninstallSolution(ctx, orgID, installationID)
	if err == nil && transitioned && installationID == s.firstID {
		s.firstDone <- pid
		select {
		case <-s.resume:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	return installation, transitioned, err
}

func requireInstallationBackendBlockedBy(t *testing.T, ctx context.Context, waiter, blocker uint32) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked bool
		err := testStore.WithControlPlane(ctx, func(ctx context.Context) error {
			return storetx.Tx(ctx).QueryRow(ctx,
				`SELECT $1::int = ANY(pg_blocking_pids($2::int))`, blocker, waiter).Scan(&blocked)
		})
		return err == nil && blocked
	}, 10*time.Second, 20*time.Millisecond, "backend %d must wait on backend %d", waiter, blocker)
}

// Without the up-front batch lock, deletion holds A plus the revision row, the
// standalone uninstall holds B and waits for that revision, and deletion then
// waits for B. Removing FOR UPDATE from ActiveInstallationIDs must make one
// participant fail with PostgreSQL's deadlock error (40P01).
func TestDeleteOrganizationSerializesWithAnotherInstallationUninstall(t *testing.T) {
	clearData(t)
	admin, orgID := mustUserAndOrg(t, testCtx, "batch@example.com", "batch-delete", "Acme")
	roleID := createRole(t, orgID, "agent", "doc", "write")
	var ids []string
	for _, name := range []string{"first", "second"} {
		targetID, _ := declarePresence(t, "batch-"+name)
		installation, err := testService.InstallSolution(testCtx, admin, &business.InstallSolutionParams{
			OrgID: orgID, AgentIdentifier: "acme.example/" + name + ":1.0.0",
			TargetID: targetID, RootScopeLabel: "Example Solution", RoleID: roleID,
		})
		require.NoError(t, err)
		ids = append(ids, installation.Id)
	}
	sort.Strings(ids)
	var org *gen.Organization
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		org, err = testStore.GetOrganization(ctx, orgID)
		return err
	}))

	barrier := &installationBatchBarrierStore{
		PostgresStore: testStore, firstID: ids[0],
		firstDone: make(chan uint32, 1), resume: make(chan struct{}),
		secondStarted: make(chan uint32, 1),
	}
	testService.SetStore(barrier)
	ctx, cancel := context.WithTimeout(testCtx, 30*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		workers.Wait()
		testService.SetStore(testStore)
		closeEveryGap(t, testCtx)
	})
	deleted, uninstalled := make(chan error, 1), make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		deleted <- testService.DeleteOrganization(ctx, admin, &gen.DeleteOrganizationRequest{
			OrgId: orgID, ConfirmSlug: org.Slug,
		})
	}()
	var deletionPID uint32
	select {
	case deletionPID = <-barrier.firstDone:
	case err := <-deleted:
		t.Fatalf("deletion did not reach its first uninstall: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		uninstalled <- testService.UninstallSolution(ctx, admin, orgID, ids[1])
	}()
	var uninstallPID uint32
	select {
	case uninstallPID = <-barrier.secondStarted:
	case err := <-uninstalled:
		t.Fatalf("standalone uninstall did not reach its installation: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	// With the fix this waits on B's installation row. With the mutation it
	// waits on the revision row while holding B, setting up the lock cycle.
	requireInstallationBackendBlockedBy(t, ctx, uninstallPID, deletionPID)
	close(barrier.resume)
	deletionErr, uninstallErr := <-deleted, <-uninstalled
	require.NoError(t, deletionErr, "deletion must commit, not abort with 40P01")
	require.NoError(t, uninstallErr, "standalone uninstall must complete, not abort with 40P01")
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		archived, err := testStore.GetOrganization(ctx, orgID)
		if err != nil {
			return err
		}
		require.NotNil(t, archived.ArchivedAt)
		for _, id := range ids {
			installation, _, err := testStore.GetInstallation(ctx, orgID, id)
			if err != nil {
				return err
			}
			require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_REVOKED, installation.Status)
		}
		return nil
	}))
}

// Report the real transactions as they enter the store; all statements and
// triggers still run in production code. The principal row lock below is the
// barrier between the standalone uninstall's grant DELETE and principal UPDATE.
type uninstallMembershipOrderStore struct {
	*infra.PostgresStore
	uninstallStarted chan uint32
	deletionStarted  chan uint32
	uninstallOnce    sync.Once
}

func (s *uninstallMembershipOrderStore) UninstallSolution(ctx context.Context, orgID, installationID string) (*gen.Installation, bool, error) {
	s.uninstallOnce.Do(func() { s.uninstallStarted <- storetx.Tx(ctx).Conn().PgConn().PID() })
	return s.PostgresStore.UninstallSolution(ctx, orgID, installationID)
}

func (s *uninstallMembershipOrderStore) ActiveInstallationIDs(ctx context.Context, orgID string) ([]string, error) {
	s.deletionStarted <- storetx.Tx(ctx).Conn().PgConn().PID()
	return s.PostgresStore.ActiveInstallationIDs(ctx, orgID)
}

// If membership deletion precedes the batch lock, it takes the authorization
// revision while the standalone uninstall holds its installation and standing
// grant. Releasing the principal barrier then closes that lock cycle (40P01),
// aborts a narrowing, and leaves its already-recorded policy receipt uncommitted.
func TestDeleteOrganizationWaitsForUninstallBeforeMembershipDeletion(t *testing.T) {
	clearData(t)
	admin, orgID := mustUserAndOrg(t, testCtx, "uninstall-order@example.com", "uninstall-order", "Acme")
	installationID, _, _ := installForExposure(t, admin, orgID)
	var org *gen.Organization
	var agentID, grantID string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		org, err = testStore.GetOrganization(ctx, orgID)
		if err != nil {
			return err
		}
		// Require an actual standing grant; an empty DELETE cannot reproduce
		// the lock inversion this regression is meant to exercise.
		return storetx.Tx(ctx).QueryRow(ctx, `
			SELECT i.agent_principal_id, g.id
			FROM installations i
			JOIN scope_nodes n ON n.id = i.root_scope_node_id
			JOIN scope_grants g ON g.org_id = i.org_id AND g.subject_kind = 'principal'
			  AND g.subject_id = i.agent_principal_id AND g.scope_path = n.scope_path
			WHERE i.id = $1 AND i.org_id = $2 AND i.status = 'active'`, installationID, orgID).Scan(&agentID, &grantID)
	}))

	store := &uninstallMembershipOrderStore{
		PostgresStore:    testStore,
		uninstallStarted: make(chan uint32, 1), deletionStarted: make(chan uint32, 1),
	}
	testService.SetStore(store)
	ctx, cancel := context.WithTimeout(testCtx, 30*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		workers.Wait()
		testService.SetStore(testStore)
		closeEveryGap(t, testCtx)
	})
	mark := testPolicyLog.mark()
	uninstalled, deleted := make(chan error, 1), make(chan error, 1)
	require.NoError(t, testStore.WithControlPlane(ctx, func(barrierCtx context.Context) error {
		tx := storetx.Tx(barrierCtx)
		var lockedID string
		if err := tx.QueryRow(barrierCtx, `SELECT id FROM principals WHERE id = $1 AND org_id = $2 FOR UPDATE`, agentID, orgID).Scan(&lockedID); err != nil {
			return err
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			uninstalled <- testService.UninstallSolution(ctx, admin, orgID, installationID)
		}()
		var uninstallPID uint32
		select {
		case uninstallPID = <-store.uninstallStarted:
		case err := <-uninstalled:
			t.Fatalf("standalone uninstall did not reach its installation: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		requireInstallationBackendBlockedBy(t, ctx, uninstallPID, tx.Conn().PgConn().PID())

		// Prove the pause is after the real grant DELETE, but before the
		// principal trigger takes the authorization revision. Neither probe
		// writes data; the failed NOWAIT probe rolls back its own transaction.
		require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
			var id string
			return storetx.Tx(ctx).QueryRow(ctx, `
				SELECT org_id FROM organization_authorization_revisions
				WHERE org_id = $1 FOR UPDATE NOWAIT`, orgID).Scan(&id)
		}))
		err := testStore.WithControlPlane(ctx, func(ctx context.Context) error {
			var id string
			return storetx.Tx(ctx).QueryRow(ctx, `SELECT id FROM scope_grants WHERE id = $1 FOR KEY SHARE NOWAIT`, grantID).Scan(&id)
		})
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, "the real standing-grant DELETE must hold its row")
		require.Equal(t, "55P03", pgErr.Code, "the standing grant must be locked by the paused uninstall")

		workers.Add(1)
		go func() {
			defer workers.Done()
			deleted <- testService.DeleteOrganization(ctx, admin, &gen.DeleteOrganizationRequest{OrgId: orgID, ConfirmSlug: org.Slug})
		}()
		var deletionPID uint32
		select {
		case deletionPID = <-store.deletionStarted:
		case err := <-deleted:
			t.Fatalf("deletion did not reach its installation batch: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		requireInstallationBackendBlockedBy(t, ctx, deletionPID, uninstallPID)
		return nil // Release the principal so uninstall and then deletion can commit.
	}))
	uninstallErr, deletionErr := <-uninstalled, <-deleted
	require.NoError(t, uninstallErr, "standalone uninstall must complete without 40P01")
	require.NoError(t, deletionErr, "organization deletion must complete without 40P01")

	entries := testPolicyLog.appended(mark)
	require.Len(t, entries, 2, "both narrowings must be witnessed")
	require.Equal(t, installationID, entries[0].SubjectID)
	require.Equal(t, orgID, entries[1].SubjectID)
	for _, entry := range entries {
		requireCommitted(t, testCtx, entry.OperationID) // Assert before cleanup can close a gap.
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		archived, err := testStore.GetOrganization(ctx, orgID)
		if err != nil {
			return err
		}
		require.NotNil(t, archived.ArchivedAt)
		installation, _, err := testStore.GetInstallation(ctx, orgID, installationID)
		if err != nil {
			return err
		}
		require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_REVOKED, installation.Status)
		var grants, members int
		var revoked bool
		if err := storetx.Tx(ctx).QueryRow(ctx, `
			SELECT (SELECT count(*) FROM scope_grants WHERE id = $1),
			       (SELECT count(*) FROM organization_members WHERE org_id = $2),
			       (SELECT revoked_at IS NOT NULL FROM principals WHERE id = $3)`, grantID, orgID, agentID).Scan(&grants, &members, &revoked); err != nil {
			return err
		}
		require.Zero(t, grants)
		require.Zero(t, members)
		require.True(t, revoked)
		return nil
	}))
}

// A status UPDATE alone is compatible with KEY SHARE, but the batch's up-front
// FOR UPDATE is not. Holding the second row this way proves target revocation
// takes the batch locks before writing, rather than relying on UPDATE's row
// locks in whatever order its plan happens to visit them.
func TestRevokeInstallationsOfTargetLocksTheBatchBeforeUpdating(t *testing.T) {
	clearData(t)
	admin, orgID := mustUserAndOrg(t, testCtx, "batch-first@example.com", "batch-first", "Acme")
	first, targetID, _ := installForExposure(t, admin, orgID)
	otherAdmin, otherOrg := mustUserAndOrg(t, testCtx, "batch-second@example.com", "batch-second", "ExampleCorp")
	roleID := createRole(t, otherOrg, "agent", "doc", "write")
	second, err := testService.InstallSolution(testCtx, otherAdmin, &business.InstallSolutionParams{
		OrgID: otherOrg, AgentIdentifier: "acme.example/exposed:1.0.0",
		TargetID: targetID, RootScopeLabel: "Example Solution", RoleID: roleID,
	})
	require.NoError(t, err)
	ids := []string{first, second.Id}
	sort.Strings(ids)
	ctx, cancel := context.WithTimeout(testCtx, 30*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	started, finished := make(chan uint32, 1), make(chan error, 1)
	var revoked []business.RevokedInstallation
	require.NoError(t, testStore.WithControlPlane(ctx, func(lockCtx context.Context) error {
		tx := storetx.Tx(lockCtx)
		if _, err := tx.Exec(lockCtx, `SELECT id FROM installations WHERE id = $1 FOR KEY SHARE`, ids[1]); err != nil {
			return err
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			finished <- testStore.WithControlPlane(ctx, func(ctx context.Context) error {
				started <- storetx.Tx(ctx).Conn().PgConn().PID()
				var err error
				revoked, err = testStore.RevokeInstallationsOfTarget(ctx, targetID, "target withdrawn", time.Now().UTC())
				return err
			})
		}()
		var batchPID uint32
		select {
		case batchPID = <-started:
		case err := <-finished:
			t.Fatalf("target revocation did not start: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		requireInstallationBackendBlockedBy(t, ctx, batchPID, tx.Conn().PgConn().PID())
		select {
		case err := <-finished:
			t.Fatalf("target revocation completed without locking the whole batch: %v", err)
		default:
		}
		return nil
	}))
	require.NoError(t, <-finished)
	require.ElementsMatch(t, []business.RevokedInstallation{
		{InstallationID: first, OrgID: orgID},
		{InstallationID: second.Id, OrgID: otherOrg},
	}, revoked)
}
