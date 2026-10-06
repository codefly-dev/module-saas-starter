package main

import (
	"context"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// `installations_target_must_be_live` takes `FOR UPDATE` on the solution target
// it reads, and the claim that comment makes is about CONCURRENCY: an install
// cannot slip past a withdrawal that has not committed yet.
//
// The claim is not provable by a single-transaction test. Reading the target
// without the lock also passes every serial test — the row is open, the trigger
// says so, the insert succeeds. What the lock changes is only visible with two
// transactions in flight: without it the inserting transaction reads the
// target's last committed version (open), the withdrawal commits, and both
// transactions succeed, leaving an ACTIVE installation of a withdrawn target —
// exactly the state the trigger exists to make unreachable.
//
// So this drives the two halves against each other on the real schema:
//
//  1. Transaction W closes the target with an UPDATE and does NOT commit. The
//     UPDATE holds the row lock.
//  2. Transaction I inserts an active installation naming that target. The
//     trigger's `FOR UPDATE` must BLOCK here rather than read the stale open row.
//  3. W commits.
//  4. I must then see the committed close and be REFUSED by name.
//
// Step 2 is where the lock is actually measured: the insert is asserted not to
// return while W is open. An FK's own lock would not produce that — PostgreSQL
// takes `FOR KEY SHARE` for a foreign key, and an UPDATE that touches no key
// column takes `FOR NO KEY UPDATE`, which does not conflict with it. The
// attribution is settled by mutation rather than by that argument: with
// `FOR UPDATE` removed from the function, step 2 returns immediately and the
// insert succeeds.
func TestMigration22TargetLivenessTriggerSerialisesAgainstAWithdrawal(t *testing.T) {
	db, url := throwawayPostgres(t)
	// The whole cutover, applied as a release applies it. Nothing is staged
	// before 20 here: this is about the trigger the migration installs, not about
	// its backfill.
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 22), url); err != nil {
		t.Fatalf("apply migrations 1..20: %v", err)
	}

	org, agent, owner, node := stageInstallationParents(t, db, "race@example.com", "race-co")
	var target string
	mustQuery(t, db, `
		INSERT INTO public.solution_targets (binding_id, solution_id, opened_generation)
		VALUES ('acme.race.binding', 'race-alias', 1) RETURNING id::text`, &target)

	ctx := context.Background()
	withdrawal, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = withdrawal.Rollback() }()
	// A tombstone generation closes the target. This is the statement the
	// reconciler runs, and it is what takes the row lock the trigger waits on.
	if _, err := withdrawal.ExecContext(ctx, `
		UPDATE public.solution_targets
		   SET closed_generation = 2, closed_at = now()
		 WHERE id = $1::uuid`, target); err != nil {
		t.Fatalf("close the target: %v", err)
	}

	// The install, on its own connection, while the withdrawal is still open.
	installed := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, `
			INSERT INTO public.installations
				(org_id, agent_principal_id, owner_principal_id, root_scope_node_id, target_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'active')`,
			org, agent, owner, node, target)
		installed <- err
	}()

	// Not a timing heuristic about how fast PostgreSQL is: the insert either
	// waits on a lock, in which case it cannot return at all until the
	// withdrawal ends, or it does not take the lock and returns as soon as the
	// trigger has read the row. Three seconds separates those two outcomes with
	// a wide margin, and the failure message names which one happened.
	select {
	case err := <-installed:
		t.Fatalf("the install did not wait for the withdrawal's row lock, so the trigger read a target that was already being closed (insert returned %v)", err)
	case <-time.After(3 * time.Second):
	}

	if err := withdrawal.Commit(); err != nil {
		t.Fatalf("commit the withdrawal: %v", err)
	}

	select {
	case err := <-installed:
		if err == nil {
			t.Fatal("the install succeeded against a target whose withdrawal had committed: an ACTIVE installation of a withdrawn target is the state the trigger exists to make unreachable")
		}
		if !strings.Contains(err.Error(), "was withdrawn at generation 2") {
			t.Fatalf("the install must be refused by the liveness trigger, naming the generation that withdrew the target; got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the install never returned after the withdrawal committed")
	}

	// And it wrote nothing. A refusal that still left the row would be the same
	// defect reached by a different path.
	var rows int
	if err := db.QueryRow(`
		SELECT count(*) FROM public.installations WHERE target_id = $1::uuid`, target).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("the refused install left %d installation row(s) naming the withdrawn target", rows)
	}
}
