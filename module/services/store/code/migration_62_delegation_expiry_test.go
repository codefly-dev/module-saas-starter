package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

func TestMigration62PreservesExistingDelegationsAndExposesRollbackLoss(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 61), databaseURL); err != nil {
		t.Fatal(err)
	}
	org, _, _, _ := stageInstallationParents(t, db, "jane@example.com", "acme")
	if _, err := db.Exec(`INSERT INTO public.authority_core_delegations
 (id, organization_id, grantor, module, purpose, scopes, revoked)
 VALUES ('existing-grant', $1, 'example-person', 'example-module', 'scheduled task', '[]', false)`, org); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerUpTo(t, 62)
	if err := migrateStoreFrom("file://"+ledger, databaseURL); err != nil {
		t.Fatal(err)
	}
	var expiry sql.NullTime
	var grantor, purpose string
	var revoked bool
	if err := db.QueryRow(`SELECT grantor, purpose, revoked, expires_at FROM public.authority_core_delegations WHERE id='existing-grant'`).Scan(&grantor, &purpose, &revoked, &expiry); err != nil {
		t.Fatal(err)
	}
	if grantor != "example-person" || purpose != "scheduled task" || revoked || expiry.Valid {
		t.Fatalf("upgrade changed the existing indefinite grant: grantor=%q purpose=%q revoked=%v expiry=%v", grantor, purpose, revoked, expiry)
	}
	if _, err := db.Exec(`UPDATE public.authority_core_delegations SET expires_at='2026-01-01T12:00:00Z' WHERE id='existing-grant'`); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if err := runner.Steps(1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT expires_at FROM public.authority_core_delegations WHERE id='existing-grant'`).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	// A down/up cycle erases the finite lifetime; it is deliberately documented
	// as data loss, never as a behavior-preserving rollback.
	if expiry.Valid {
		t.Fatal("down/up unexpectedly retained a timestamp after dropping its column")
	}
}
