package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

func TestMigration61PreservesIndependentPolicyReceiptsOnRollback(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 60), databaseURL); err != nil {
		t.Fatal(err)
	}
	org, agent, owner, _ := stageInstallationParents(t, db, "jane@example.com", "acme")
	const installation = "00000000-0000-0000-0000-000000000001"
	digest := "sha256:" + strings.Repeat("ab", 32)
	insert := func(policy string) error {
		_, err := db.Exec(`INSERT INTO public.executable_artifact_approvals
   (id,org_id,installation_id,module_principal_id,policy_id,subject_digest,subject,approved_by)
   VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,'identity'::bytea,$6)`, org, installation, agent, policy, digest, owner)
		return err
	}
	if err := insert("example.intent"); err != nil {
		t.Fatal(err)
	}
	var original string
	mustQuery(t, db, `SELECT id::text FROM public.executable_artifact_approvals`, &original)
	ledger := ledgerUpTo(t, 61)
	if err := migrateStoreFrom("file://"+ledger, databaseURL); err != nil {
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
	// A compatible rollback retains existing consent and restores old uniqueness.
	if err := runner.Steps(-1); err != nil {
		t.Fatal(err)
	}
	if err := insert("example.preview"); err == nil {
		t.Fatal("old schema accepted independent policy identity")
	}
	if err := runner.Steps(1); err != nil {
		t.Fatal(err)
	}
	if err := insert("example.preview"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Steps(-1); err == nil {
		t.Fatal("rollback discarded or merged independent consent")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM public.executable_artifact_approvals`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rollback retained %d receipts, want both", count)
	}
	var retained string
	mustQuery(t, db, `SELECT id::text FROM public.executable_artifact_approvals WHERE policy_id='example.intent'`, &retained)
	if retained != original {
		t.Fatal("migration changed the original approval identity")
	}
	// Failed ALTER TABLE is atomic: same-policy duplicates remain impossible.
	if err := insert("example.intent"); err == nil {
		t.Fatal("failed rollback removed consent uniqueness")
	}
}
