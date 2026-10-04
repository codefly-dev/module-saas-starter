package main

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
)

// Like the migration-20 replay, install the proposed ledger from zero, stage
// pre-cutover rows, then apply only the new version. The URL must name a fresh,
// disposable PostgreSQL 16+ cluster: migration 1 also requires absent app roles.
func TestMigration23ColdCutoverFromZero(t *testing.T) {
	raw := os.Getenv("ACCOUNTS_INSTALLER_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("ACCOUNTS_INSTALLER_TEST_DATABASE_URL is unset; PostgreSQL replay not run")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		(parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1") ||
		!strings.HasPrefix(parsed.Path, "/installer_test_") {
		t.Fatal("requires a disposable loopback installer_test_ database")
	}
	db, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var existing int
	if err := db.QueryRow(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind IN ('r','p')`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing != 0 {
		t.Fatal("from-zero replay refuses a database containing application tables")
	}
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 22), raw); err != nil {
		t.Fatalf("apply migrations 1..22: %v", err)
	}
	t.Log("applied migrations 1..22 from zero")
	execColdCutoverSQL(t, db, `INSERT INTO public.solution_targets (binding_id,solution_id,opened_generation)
		VALUES ('acme.test.example','declared',1),('acme.test.removed','removed',1)`)
	execColdCutoverSQL(t, db, `INSERT INTO public.solution_registrations
		(solution_id,publisher,revision,frontend_revision,frontend_manifest,frontend_lease_expires_at,
		 backend_revision,backend_upstream,backend_service_alias,backend_lease_expires_at,
		 declared_binding_id,declared_generation,declared_release,declared_target_id)
		VALUES ('runtime','solution:runtime',nextval('public.solution_registry_revision_sequence'),
		1,'{}',now()+interval '1 hour',1,'http://example.svc','runtime',now()+interval '1 hour',NULL,NULL,NULL,NULL),
		('declared','solution:declared',nextval('public.solution_registry_revision_sequence'),
		1,'{}',now()+interval '1 hour',1,'http://example.svc','declared',now()+interval '1 hour','acme.test.example',1,'acme/example@1.0.0',(SELECT id FROM public.solution_targets WHERE binding_id='acme.test.example'))`)
	execColdCutoverSQL(t, db, `INSERT INTO public.solution_registrations
		(solution_id,publisher,revision,tombstoned_at,declared_binding_id,declared_generation,declared_release,declared_target_id)
		VALUES ('removed','solution:removed',nextval('public.solution_registry_revision_sequence'),now(),'acme.test.removed',2,'acme/example@1.0.0',(SELECT id FROM public.solution_targets WHERE binding_id='acme.test.removed'))`)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 23), raw); err != nil {
		t.Fatalf("apply migration 23: %v", err)
	}
	assertCount := func(query string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("query %s returned %d, want %d", query, got, want)
		}
	}
	assertCount(`SELECT count(*) FROM information_schema.columns WHERE table_schema='public'
		AND table_name='solution_registrations' AND column_name LIKE '%lease%'`, 0)
	assertCount(`SELECT count(*) FROM pg_indexes WHERE schemaname='public'
		AND tablename='solution_registrations' AND indexdef LIKE '%lease%'`, 0)
	assertCount(`SELECT count(*) FROM public.solution_registrations WHERE solution_id='runtime'`, 0)
	assertCount(`SELECT count(*) FROM public.solution_registrations WHERE solution_id='declared'
		AND declared_binding_id='acme.test.example' AND frontend_revision IS NULL AND backend_revision IS NULL`, 1)
	assertCount(`SELECT count(*) FROM public.solution_registrations WHERE solution_id='removed' AND tombstoned_at IS NOT NULL`, 1)
	// The retained projection accepts whole observations without a lease, while
	// partial observations and undeclared presence fail at the database boundary.
	execColdCutoverSQL(t, db, `UPDATE public.solution_registrations SET frontend_revision=10,frontend_manifest='{}',
		backend_revision=10,backend_upstream='http://example.svc',backend_service_alias='declared'
		WHERE solution_id='declared'`)
	for _, query := range []string{
		`UPDATE public.solution_registrations SET frontend_manifest=NULL WHERE solution_id='declared'`,
		`UPDATE public.solution_registrations SET backend_upstream=NULL WHERE solution_id='declared'`,
		`INSERT INTO public.solution_registrations (solution_id,publisher,revision) VALUES ('runtime','solution:runtime',11)`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("invalid registry state accepted: %s", query)
		}
	}
	t.Log("migration 23 deleted runtime presence and lease columns; declared presence, tombstones and whole-half constraints verified")
}

func execColdCutoverSQL(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}
