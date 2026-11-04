package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

// The two settings the platform's Postgres library binds a scoped transaction
// to, which migration 65 renames every policy and function body onto.
const (
	libraryTenantSetting = "codefly.current_tenant_id"
	libraryUserSetting   = "codefly.current_user_id"
	hostTenantSetting    = "app.current_org_id"
	hostUserSetting      = "app.current_user_id"
)

// Migration 65 is a rename and must be nothing else. Proving that by reading
// its SQL proves only that it says so; this stands a database at version 64,
// fingerprints every row-security decision in the catalog, applies 65, and
// requires the fingerprint to equal the old one with the two setting names
// substituted and no other difference.
//
// That one equality carries every claim at once. A policy whose USING or WITH
// CHECK shape, verb, PERMISSIVE/RESTRICTIVE kind or role list moved shows up as
// a differing line. A policy the migration MISSED shows up too, and this is the
// half a reading of the file cannot give: the catalog's copy still says
// app.current_org_id where the substituted fingerprint says
// codefly.current_tenant_id. A missed policy does not fail at runtime — nothing
// binds the old name any more, so current_setting returns NULL, the predicate
// is false and the relation simply reads as empty for every caller — so a
// suite that only exercises the renamed tables would stay green through it.
func TestMigration65RenamesEveryScopeSettingAndNothingElse(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 64), databaseURL); err != nil {
		t.Fatal(err)
	}

	before := rowSecurityCatalog(t, db)
	if !strings.Contains(before, hostTenantSetting) || !strings.Contains(before, hostUserSetting) {
		t.Fatal("version 64 is expected to read the host's own settings; the fingerprint query found neither, so it proves nothing")
	}
	want := strings.NewReplacer(
		hostTenantSetting, libraryTenantSetting,
		hostUserSetting, libraryUserSetting,
	).Replace(before)

	// Two organisations, staged while the superuser still bypasses RLS.
	orgA, _, _, _ := stageInstallationParents(t, db, "jane@example.com", "acme")
	orgB, _, _, _ := stageInstallationParents(t, db, "john@example.com", "example-two")

	ledger := ledgerUpTo(t, 65)
	if err := migrateStoreFrom("file://"+ledger, databaseURL); err != nil {
		t.Fatal(err)
	}

	after := rowSecurityCatalog(t, db)
	if after != want {
		t.Fatalf("migration 65 is not a pure rename:\n%s", firstDifference(want, after))
	}
	if strings.Contains(after, hostTenantSetting) || strings.Contains(after, hostUserSetting) {
		t.Fatal("a policy or function body still reads the host's old setting names, which nothing binds any more, so it reads as empty")
	}

	// And the renamed policies isolate: the library's setting admits the bound
	// organisation's row and no other, and an unbound transaction sees none.
	// Reading one tenant's row proves admission; a count proves nothing about
	// WHICH row was admitted.
	if got := scopedOrganizationIDs(t, db, libraryTenantSetting, orgA); got != orgA {
		t.Fatalf("a transaction bound to %s saw %q, want only %q", libraryTenantSetting, got, orgA)
	}
	if got := scopedOrganizationIDs(t, db, libraryTenantSetting, orgB); got != orgB {
		t.Fatalf("a transaction bound to %s saw %q, want only %q", libraryTenantSetting, got, orgB)
	}
	// Fail-closed, and in the one way that matters: binding the name the host
	// used to read admits nothing, rather than everything.
	if got := scopedOrganizationIDs(t, db, hostTenantSetting, orgA); got != "" {
		t.Fatalf("binding the retired %s admitted %q; the rename must leave it inert", hostTenantSetting, got)
	}

	// The down half restores the catalog it found, exactly.
	abs, err := filepath.Abs(ledger)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.New("file://"+abs, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = runner.Close() }()
	if err := runner.Steps(-1); err != nil {
		t.Fatal(err)
	}
	if rolled := rowSecurityCatalog(t, db); rolled != before {
		t.Fatalf("the down half did not restore version 64's catalog:\n%s", firstDifference(before, rolled))
	}
	if err := runner.Steps(1); err != nil {
		t.Fatal(err)
	}
	if replayed := rowSecurityCatalog(t, db); replayed != after {
		t.Fatalf("replaying the up half did not reproduce it:\n%s", firstDifference(after, replayed))
	}
}

// rowSecurityCatalog is every row-security decision in the database, as the
// catalog renders it: each relation's ENABLE and FORCE state, each policy with
// its kind, verb, role list and both expressions, and each routine body — every
// place a scope setting can be read. Expressions are rendered under an empty
// search_path, which is what makes the deparser schema-qualify, so the text is
// comparable to the ledger's own.
//
// It also carries each relation's and routine's owner and privileges, which the
// rename has no business changing. CREATE OR REPLACE FUNCTION is documented to
// keep both, and DROP POLICY / CREATE POLICY never touches a grant — but a
// migration that silently widened a grant while renaming a setting is exactly
// the failure a reviewer cannot see in a thousand generated lines, so these are
// compared rather than assumed.
func rowSecurityCatalog(t *testing.T, db *sql.DB) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SELECT pg_catalog.set_config('search_path', '', false)`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(`
SELECT line FROM (
    SELECT 'rls ' || c.oid::regclass::text || ' enabled=' || c.relrowsecurity::text || ' forced=' || c.relforcerowsecurity::text AS line
      FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND (c.relrowsecurity OR c.relforcerowsecurity)
    UNION ALL
    SELECT 'policy ' || p.polrelid::regclass::text || ' ' || p.polname
           || ' kind=' || CASE WHEN p.polpermissive THEN 'PERMISSIVE' ELSE 'RESTRICTIVE' END
           || ' verb=' || p.polcmd::text
           || ' roles=' || coalesce((SELECT string_agg(CASE WHEN g.oid = 0 THEN 'PUBLIC' ELSE r.rolname::text END, ',' ORDER BY g.ord)
                                       FROM unnest(p.polroles) WITH ORDINALITY AS g(oid, ord)
                                       LEFT JOIN pg_catalog.pg_roles r ON r.oid = g.oid), '')
           || ' using=' || coalesce(pg_get_expr(p.polqual, p.polrelid), '<none>')
           || ' check=' || coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '<none>')
      FROM pg_catalog.pg_policy p
    UNION ALL
    SELECT 'routine ' || p.oid::regprocedure::text
           || ' owner=' || pg_get_userbyid(p.proowner)
           || ' acl=' || coalesce(p.proacl::text, '<default>')
           || ' definer=' || p.prosecdef::text
           || ' ' || p.prosrc
      FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public'
    UNION ALL
    SELECT 'grants ' || c.oid::regclass::text
           || ' owner=' || pg_get_userbyid(c.relowner)
           || ' acl=' || coalesce(c.relacl::text, '<default>')
      FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
) catalog ORDER BY line`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("the row-security catalog came back empty; the fingerprint query is wrong")
	}
	return strings.Join(out, "\n")
}

// scopedOrganizationIDs is every organization id a request transaction sees
// with `setting` bound to `value`: as app_tenant, which holds no BYPASSRLS, in
// a transaction of its own so the binding cannot outlive it.
func scopedOrganizationIDs(t *testing.T, db *sql.DB, setting, value string) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SET LOCAL ROLE app_tenant`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT pg_catalog.set_config($1, $2, true)`, setting, value); err != nil {
		t.Fatal(err)
	}
	var seen string
	if err := tx.QueryRow(`SELECT coalesce(string_agg(id::text, ',' ORDER BY id::text), '') FROM public.organizations`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	return seen
}
