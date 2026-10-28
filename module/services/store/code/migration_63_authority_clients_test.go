package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
)

func TestMigration63PreservesModuleDelegationsAndConfinesClientMetadata(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 62), databaseURL); err != nil {
		t.Fatal(err)
	}
	org, _, _, _ := stageInstallationParents(t, db, "jane@example.com", "acme")
	expires := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO public.authority_core_delegations
 (id, organization_id, grantor, module, purpose, scopes, revoked, expires_at)
 VALUES ('existing-module-grant', $1, 'example-person', 'example-module', 'scheduled task', '[]', false, $2)`, org, expires); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerUpTo(t, 63)
	if err := migrateStoreFrom("file://"+ledger, databaseURL); err != nil {
		t.Fatal(err)
	}
	var module, client, audience, resource string
	var gotExpiry time.Time
	if err := db.QueryRow(`SELECT module, client_id, audience, resource, expires_at
 FROM public.authority_core_delegations WHERE id='existing-module-grant'`).Scan(&module, &client, &audience, &resource, &gotExpiry); err != nil {
		t.Fatal(err)
	}
	if module != "example-module" || client != "" || audience != "" || resource != "" || !expires.Equal(gotExpiry) {
		t.Fatalf("migration changed the existing module grant: %q %q %q %q %v", module, client, audience, resource, gotExpiry)
	}
	for _, role := range []string{"app_tenant", "app_control_plane"} {
		for _, operation := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			var allowed bool
			if err := db.QueryRow(`SELECT pg_catalog.has_table_privilege($1, 'public.authority_core_clients', $2)`, role, operation).Scan(&allowed); err != nil {
				t.Fatal(err)
			}
			if want := role == "app_control_plane" && operation != "DELETE"; allowed != want {
				t.Fatalf("%s %s = %v, want %v", role, operation, allowed, want)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO public.authority_core_clients
 (id, name, redirect_uris, revision, disabled)
 VALUES ('client:example', 'Example Client', ARRAY['https://example.com/callback'], 18446744073709551615, true)`); err != nil {
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
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM public.authority_core_clients`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("down/up unexpectedly retained client metadata after dropping its table")
	}
}
