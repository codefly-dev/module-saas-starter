package main

import (
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

// HOST-INST-002, HOST-INST-004, HOST-INST-005: run the real numbered migration
// and its down, retaining unacknowledged rows as revoked rather than activating
// them on downgrade. Uses the same disposable-cluster harness as migration 23.
func TestMigration36InstallationPendingAndDown(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 35), databaseURL); err != nil {
		t.Fatal(err)
	}
	org, agent, owner, node := stageInstallationParents(t, db, "installer@example.com", "example-installation")
	execColdCutoverSQL(t, db, `INSERT INTO solution_host_bindings(binding_id,host_coordinate,host_component)
		VALUES ('acme.test.pending','example-host','example-component')`)
	var target string
	mustQuery(t, db, `INSERT INTO solution_targets(binding_id,solution_id,opened_generation)
		VALUES ('acme.test.pending','example',1) RETURNING id::text`, &target)
	ledger := ledgerUpTo(t, 36)
	if err := migrateStoreFrom("file://"+ledger, databaseURL); err != nil {
		t.Fatal(err)
	}
	var id string
	insert := `INSERT INTO installations(org_id,agent_principal_id,owner_principal_id,root_scope_node_id,target_id)
		VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid) RETURNING id::text`
	mustQuery(t, db, insert, &id, org, agent, owner, node, target)
	if got := statusOf(t, db, id); got != "pending" {
		t.Fatalf("new installation status=%s, want pending", got)
	}
	if _, err := db.Exec(insert, org, agent, owner, node, target); err == nil {
		t.Fatal("pending installation did not reserve its target")
	}
	if _, err := db.Exec(`UPDATE installations SET failure_reason='Example reason' WHERE id=$1`, id); err == nil {
		t.Fatal("a pending installation accepted a failure reason")
	}
	if _, err := db.Exec(`UPDATE installations SET status='failed',failure_reason='Example refusal' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, org, agent, owner, node, target); err == nil {
		t.Fatal("failed installation did not reserve its target")
	}
	if _, err := db.Exec(`UPDATE installations SET failure_reason=repeat('x',513) WHERE id=$1`, id); err == nil {
		t.Fatal("unbounded reason accepted")
	}
	if _, err := db.Exec(`UPDATE installations SET status='pending',failure_reason=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var revision string
	mustQuery(t, db, `SELECT revision::text FROM installations WHERE id=$1`, &revision, id)
	if revision != "1" {
		t.Fatalf("refusal/retry changed consent revision to %s", revision)
	}
	for _, role := range []string{"app_control_plane", "app_job_worker"} {
		var allowed bool
		if err := db.QueryRow(`SELECT has_schema_privilege($1,'public','CREATE')`, role).Scan(&allowed); err != nil || allowed {
			t.Fatalf("runtime schema CREATE for %s: allowed=%v err=%v", role, allowed, err)
		}
	}
	abs, err := filepath.Abs(ledger)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.New("file://"+abs, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("migration 36 down: %v", err)
	}
	if got := statusOf(t, db, id); got != "revoked" {
		t.Fatalf("downgrade made unacknowledged installation %s", got)
	}
	var columns int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema='public'
		AND table_name='installations' AND column_name IN ('failure_reason','install_attempts','install_job_id')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("downgrade left lifecycle columns: %d err=%v", columns, err)
	}
}
