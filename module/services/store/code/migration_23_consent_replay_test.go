package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// Migration 22's backfill, replayed from zero against staged pre-cutover rows.
//
// WHY THIS CANNOT BE TESTED THE ORDINARY WAY. The store's runner keys on
// migration VERSION with no content checksum (`main.go` reads the applied
// version and only errors when that file is ABSENT). So editing an
// already-applied migration in place is silently skipped: the database keeps the
// old SQL, nothing goes dirty, nothing errors, and the suite passes while testing
// the previous schema. A green run after such an edit is not evidence about the
// edit.
//
// This test therefore drops nothing and reuses nothing: it starts a throwaway
// PostgreSQL 16, applies migrations 1..19, stages the rows that only exist
// BEFORE the cutover, then applies 20 alone and reads the disposition back.
//
// WHAT IT PROVES. The backfill matched `solution_targets.solution_id =
// installations.solution_identifier` on any LIVE target. A route alias is a
// reusable property of a target, never its identity — so if a binding was
// withdrawn and a different one later took the same alias, the live target
// belonged to the SECOND binding and an installation that consented to the FIRST
// silently became an installation of the second. Same alias, different solution,
// consent moved with nobody acting, directly beneath a comment saying this
// migration exists to prevent exactly that.
//
// `i.created_at >= t.opened_at` is the fix: the installation must have been
// created during the period this target has been open.
//
// WHAT IT DOES NOT PROVE, said here because the migration's own comment is
// easily read as stronger than it is: the predicate NARROWS the carry-over, it
// does not establish historical consent. `created_at` records when the
// installation row was written, not which binding the administrator was looking
// at; an alias that was reused while a single target period stayed open, or a
// clock that moved, are both outside what a timestamp can answer. What is proved
// below is the one direction that matters for a disposition nobody will review
// case by case: an installation that predates the live period is never carried
// into it. Erring the other way (a carry that should have been a revoke) is not
// detectable from this schema at all, and the fail-closed second statement is
// the only answer to it.
func TestMigration22DoesNotTransferConsentAcrossAnAliasReuse(t *testing.T) {
	db, url := throwawayPostgres(t)

	// Two ledgers: 1..19, then 1..20. The second CONTAINS the first, because the
	// runner reads the database's current version and requires that migration's
	// file to be present in the source — a directory holding only 20 is refused
	// with "database is at migration 21, which this ledger does not contain",
	// which the third run of this test discovered. Containing it is also what a
	// real upgrade looks like: the runner skips what is applied and runs 20.
	//
	// Nothing past 20 is copied. 21 and 22 are irrelevant here and applying them
	// would widen what a failure could mean.
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 21), url); err != nil {
		t.Fatalf("apply migrations 1..19: %v", err)
	}

	// Stage the alias reuse. One alias, two periods: the first target closed,
	// the second live. `kept` was created inside the live target's period and
	// must carry over; `moved` predates it — it consented to the closed period,
	// and carrying it would be the transfer.
	const alias = "shared-alias"
	var closedTarget, liveTarget string
	mustQuery(t, db, `
		INSERT INTO public.solution_targets (binding_id, solution_id, opened_generation, opened_at, closed_generation, closed_at)
		VALUES ('acme.test.first', $1, 1, now() - interval '10 days', 2, now() - interval '5 days')
		RETURNING id::text`, &closedTarget, alias)
	mustQuery(t, db, `
		INSERT INTO public.solution_targets (binding_id, solution_id, opened_generation, opened_at)
		VALUES ('acme.test.second', $1, 1, now() - interval '2 days')
		RETURNING id::text`, &liveTarget, alias)

	// TWO organisations, because `idx_installations_active_solution` is UNIQUE on
	// (org_id, solution_identifier) for active rows — one organisation cannot
	// hold two active installations of the same alias, which the second run of
	// this test discovered. Two organisations is also the realistic shape: both
	// installed the same alias, one before the alias was reused and one after.
	//
	// The FK chain each needs: users → organizations, plus two principals and a
	// scope node per organisation. Staged explicitly rather than with
	// gen_random_uuid(), which the FIRST run discovered — `installations_org_id_fkey`.
	earlyOrg, earlyAgent, earlyOwner, earlyNode := stageInstallationParents(t, db, "early@example.com", "early-co")
	lateOrg, lateAgent, lateOwner, lateNode := stageInstallationParents(t, db, "late@example.com", "late-co")

	moved := stageInstallation(t, db, alias, "now() - interval '7 days'",
		earlyOrg, earlyAgent, earlyOwner, earlyNode)
	kept := stageInstallation(t, db, alias, "now() - interval '1 day'",
		lateOrg, lateAgent, lateOwner, lateNode)

	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 22), url); err != nil {
		t.Fatalf("apply migration 22: %v", err)
	}

	// `kept` carries over to the live target it was created under.
	if status := statusOf(t, db, kept); status != "active" {
		t.Fatalf("an installation created inside the live target's period must stay active, got %q", status)
	}
	if keptTarget := targetOf(t, db, kept); keptTarget != liveTarget {
		t.Fatalf("expected the live target %s, got %q", liveTarget, keptTarget)
	}

	// `moved` is REVOKED, not carried. This is the assertion the old SQL fails:
	// it would have set target_id to the live target of a different binding.
	movedTarget := targetOf(t, db, moved)
	if status := statusOf(t, db, moved); status != "revoked" {
		t.Fatalf("an installation predating the live target consented to a CLOSED period; it must be revoked, got %q (target %q)",
			status, movedTarget)
	}
	if movedTarget == liveTarget {
		t.Fatal("consent was transferred across an alias reuse: the installation now names a target it never consented to")
	}
	// The closed period's target is not a consolation prize either: the
	// disposition is a revocation with a recorded reason, not a carry onto
	// whichever row happens to match the alias.
	if movedTarget != "" {
		t.Fatalf("a revoked installation names no target, got %q (the live period is %s, the closed one %s)",
			movedTarget, liveTarget, closedTarget)
	}
	var reason string
	mustQuery(t, db, `
		SELECT COALESCE(revoked_reason, '') FROM public.installations WHERE id = $1::uuid`, &reason, moved)
	if !strings.Contains(reason, "no live solution target") {
		t.Fatalf("a revocation this migration performed states why, got %q", reason)
	}
}

// throwawayPostgres starts a PostgreSQL 16 for one test and returns a connection
// and its URL. Each test gets its own cluster: these tests apply PARTIAL ledgers
// and stage rows that only exist before a cutover, so a shared cluster would let
// a failure in either be attributed to the other.
//
// Skipped unless the migration replay gate asked for it. `migration-reference-gate.mjs
// --replay` is the one CI step that declares `postgres:16` as an external input
// and has a container runtime; a bare `go test ./...` in this package must not
// start depending on docker, so the gate's signal is the switch. The gate selects
// `-run '^TestMigration'`, which is why every test here carries that prefix.
func throwawayPostgres(t *testing.T) (*sql.DB, string) {
	t.Helper()
	if os.Getenv("MIGRATION_CONSENT_REPLAY") == "" {
		t.Skip("needs a container runtime; run through migration-reference-gate.mjs <reference> --replay")
	}
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// Pull up front: `docker run -d` interleaves pull progress with the id, so
	// the id is only reliably the last line once the image is local.
	docker("pull", "postgres:16")
	started := docker("run", "--rm", "-d", "-e", "POSTGRES_PASSWORD=example",
		"-p", "127.0.0.1::5432", "postgres:16")
	id := started[strings.LastIndex(started, "\n")+1:]
	t.Cleanup(func() { docker("rm", "-f", id) })

	port := strings.TrimPrefix(docker("port", id, "5432/tcp"), "127.0.0.1:")
	url := "postgres://postgres:example@127.0.0.1:" + port + "/postgres?sslmode=disable"
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Generously long, and deliberately not tuned to how fast this starts on an
	// idle machine. A readiness timeout here says nothing about the migration
	// under test, so a short one converts "the host was busy" into a red that
	// reads as a schema failure — which is what the fourth run of this test did,
	// with four agents starting containers at once. The last ping error is
	// reported so a genuine refusal is still distinguishable from slowness.
	deadline := time.Now().Add(5 * time.Minute)
	for {
		err := db.Ping()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres did not become ready within %s (last error: %v); on a contended machine this is start-up time, not the schema",
				5*time.Minute, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return db, url
}

// ledgerUpTo copies the proposed ledger's migrations through `highest` into a
// temporary directory, so a test can stand a database at an intermediate version
// and then apply one migration onto it.
//
// It reads `../migrations` directly rather than a git archive, which is the
// point: an in-place edit of an applied migration is what this shape exists to
// make observable, so the files under test have to be the working tree's.
func ledgerUpTo(t *testing.T, highest int) string {
	t.Helper()
	source, err := filepath.Abs("../migrations")
	if err != nil {
		t.Fatal(err)
	}
	into := t.TempDir()
	for _, file := range listSQL(t, source) {
		if versionOf(t, file) > highest {
			continue
		}
		body, err := os.ReadFile(filepath.Join(source, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(into, file), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return into
}

// stageInstallationParents creates the one organisation, two principals and
// scope node that an installation's foreign keys require.
func stageInstallationParents(t *testing.T, db *sql.DB, email, slug string) (org, agent, owner, node string) {
	t.Helper()
	var user string
	mustQuery(t, db, `
		INSERT INTO public.users (uuid, primary_email)
		VALUES (gen_random_uuid(), $1) RETURNING uuid::text`, &user, email)
	mustQuery(t, db, `
		INSERT INTO public.organizations (id, name, slug, owner_id)
		VALUES (gen_random_uuid(), $1, $1, $2::uuid) RETURNING id::text`, &org, slug, user)
	// An agent principal needs an agent_identifier and an org (principals_agent_identifier_consistency,
	// principals_org_scope); a service principal needs an org and no identifier.
	mustQuery(t, db, `
		INSERT INTO public.principals (id, kind, display_name, org_id, agent_identifier)
		VALUES (gen_random_uuid(), 'agent', 'staged agent', $1::uuid, 'acme.example/staged:1.0.0')
		RETURNING id::text`, &agent, org)
	mustQuery(t, db, `
		INSERT INTO public.principals (id, kind, display_name, org_id)
		VALUES (gen_random_uuid(), 'service', 'staged owner', $1::uuid) RETURNING id::text`, &owner, org)
	mustQuery(t, db, `
		INSERT INTO public.scope_nodes (id, org_id, scope_path, kind, label)
		VALUES (gen_random_uuid(), $1::uuid, 'cutover'::public.ltree, 'solution', 'Cutover Solution')
		RETURNING id::text`, &node, org)
	return org, agent, owner, node
}

func stageInstallation(t *testing.T, db *sql.DB, alias, createdAt, org, agent, owner, node string) string {
	t.Helper()
	var id string
	// The columns migration 23 reads, on real parent rows.
	mustQuery(t, db, `
		INSERT INTO public.installations
			(org_id, agent_principal_id, solution_identifier, owner_principal_id, root_scope_node_id, status, created_at)
		VALUES ($2::uuid, $3::uuid, $1, $4::uuid, $5::uuid, 'active', `+createdAt+`)
		RETURNING id::text`, &id, alias, org, agent, owner, node)
	return id
}

func statusOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	mustQuery(t, db, `SELECT status FROM public.installations WHERE id = $1::uuid`, &status, id)
	return status
}

func targetOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var target string
	mustQuery(t, db, `SELECT COALESCE(target_id::text, '') FROM public.installations WHERE id = $1::uuid`, &target, id)
	return target
}

func mustQuery(t *testing.T, db *sql.DB, query string, into *string, args ...any) {
	t.Helper()
	if err := db.QueryRow(query, args...).Scan(into); err != nil {
		t.Fatalf("%s: %v", summarize(query), err)
	}
}

// summarize names the failing statement in one line. It takes the first
// non-empty line rather than indexing into the split, which panicked on a
// single-line query and reported a nil-pointer index out of range in place of
// the database error it was called to show.
func summarize(query string) string {
	for _, line := range strings.Split(query, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return "<empty query>"
}
