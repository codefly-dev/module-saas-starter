package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

// A read of one scope setting, in every spelling the ledger and the deparser
// produce: optionally schema-qualified, with or without the ::text cast. Group 1
// is the qualification and group 2 the cast, which the rewrite keeps.
func scopeReadPattern(setting string) *regexp.Regexp {
	return regexp.MustCompile(`((?:pg_catalog\.)?)current_setting\('` +
		regexp.QuoteMeta(setting) + `'(::text)?, true\)`)
}

// compatibilityWindow is what migration 66 must produce, re-derived here rather
// than read from the migration: every read of a library setting becomes a read
// of it with the host's retired name behind it. Written as a substitution over
// the whole catalog fingerprint, so it carries policy predicates and routine
// bodies alike.
func compatibilityWindow(catalog string) string {
	for _, pair := range [][2]string{
		{libraryTenantSetting, hostTenantSetting},
		{libraryUserSetting, hostUserSetting},
	} {
		catalog = scopeReadPattern(pair[0]).ReplaceAllString(catalog,
			`COALESCE(NULLIF(${1}current_setting('`+pair[0]+`'${2}, true), ''::text), `+
				`${1}current_setting('`+pair[1]+`'${2}, true))`)
	}
	return catalog
}

// Migration 65 renamed the two scope settings in one step, and a one-step
// rename of an RLS setting is not deployable. The policy is
// `col::text = current_setting(<name>, true)`, so a binary that binds the other
// name reads NULL: every scoped SELECT returns zero rows with no error and
// every INSERT fails its WITH CHECK. The store's bootstrap job applies the
// migration before the new accounts ReplicaSet is ready, so a rolling update
// puts old pods against the renamed catalog; rolling the image back with the
// migration applied leaves the whole fleet there indefinitely. Nothing raises.
//
// Migration 66 is the transition that was missing: every predicate reads both
// names, the library's first. This stands a database at 65, applies 66, and
// requires three things of it — that the catalog changed in exactly that one
// way and nothing else, that a transaction binding EITHER name is admitted to
// its own tenant and no other, and that binding neither is still empty.
//
// The admission half is what a reading of the SQL cannot give. A predicate that
// reads both names and admits the wrong tenant, or every tenant, is a correct
// looking COALESCE away.
func TestMigration66OpensTheScopeSettingCompatibilityWindow(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 65), databaseURL); err != nil {
		t.Fatal(err)
	}

	before := rowSecurityCatalog(t, db)
	if !strings.Contains(before, libraryTenantSetting) || !strings.Contains(before, libraryUserSetting) {
		t.Fatal("version 65 is expected to read the library's two settings; the fingerprint query found neither, so this proves nothing")
	}
	if strings.Contains(before, hostTenantSetting) || strings.Contains(before, hostUserSetting) {
		t.Fatal("version 65 is expected to read the library's settings alone; something already reads a retired name")
	}

	// Two organisations, staged while the superuser still bypasses RLS.
	orgA, _, _, _ := stageInstallationParents(t, db, "jane@example.com", "acme")
	orgB, _, _, _ := stageInstallationParents(t, db, "john@example.com", "example-two")

	// The skew, at version 65: a pod that binds the retired name — any image
	// built before the rename — is admitted to nothing at all. This is the
	// failure 66 exists to remove, and asserting it here is what keeps the
	// assertion after the migration from passing for some other reason.
	if seen := organizationsVisibleWith(t, db, hostTenantSetting, orgA); seen != "" {
		t.Fatalf("at version 65 the retired %s admitted %q; the skew this migration addresses does not exist as described", hostTenantSetting, seen)
	}

	ledger := ledgerUpTo(t, 66)
	if err := migrateStoreFrom("file://"+ledger, databaseURL); err != nil {
		t.Fatal(err)
	}

	after := rowSecurityCatalog(t, db)
	if want := compatibilityWindow(before); after != want {
		t.Fatalf("migration 66 is not exactly the compatibility window over version 65's catalog:\n%s", firstDifference(want, after))
	}
	// The substitution above would also be satisfied by a catalog that reads
	// neither name, so require the window to be there in its own right.
	if !strings.Contains(after, hostTenantSetting) || !strings.Contains(after, libraryTenantSetting) {
		t.Fatal("the rewritten catalog does not read both tenant settings")
	}

	// A pod on either side of the rename reads its own tenant's rows, and only
	// those. A count would prove nothing about WHICH row was admitted.
	if seen := organizationsVisibleWith(t, db, libraryTenantSetting, orgA); seen != orgA {
		t.Fatalf("a transaction binding %s alone saw %q, want only %q", libraryTenantSetting, seen, orgA)
	}
	if seen := organizationsVisibleWith(t, db, hostTenantSetting, orgB); seen != orgB {
		t.Fatalf("a transaction binding the retired %s alone saw %q, want only %q", hostTenantSetting, seen, orgB)
	}
	// The host binds both halves of both names. The library's name wins, so a
	// stale value under the retired one cannot widen or redirect a request.
	if seen := organizationsVisibleWith(t, db,
		libraryTenantSetting, orgA, hostTenantSetting, orgB); seen != orgA {
		t.Fatalf("with %s=%s and %s=%s the transaction saw %q, want only %q — the library's name must win",
			libraryTenantSetting, "A", hostTenantSetting, "B", seen, orgA)
	}
	// Fail-closed, in both the ways a request transaction reaches it: nothing
	// bound at all, and both halves bound empty, which is what WithOrgTx does
	// to the user half and WithUserTx to the tenant half.
	if seen := organizationsVisibleWith(t, db); seen != "" {
		t.Fatalf("an unbound transaction saw %q, want nothing", seen)
	}
	if seen := organizationsVisibleWith(t, db,
		libraryTenantSetting, "", hostTenantSetting, ""); seen != "" {
		t.Fatalf("a transaction with both tenant settings bound empty saw %q, want nothing", seen)
	}

	// And the WITH CHECK half, which a SELECT never exercises: a write under
	// the retired binding alone is admitted for its own tenant and refused for
	// the other. Under forced RLS a refused UPDATE is not an error — it matches
	// zero rows — so the row count is the assertion.
	if affected := organizationsRenamedWith(t, db, orgA, hostTenantSetting, orgA); affected != 1 {
		t.Fatalf("an UPDATE bound to the retired %s changed %d rows of its own organisation, want 1", hostTenantSetting, affected)
	}
	if affected := organizationsRenamedWith(t, db, orgB, hostTenantSetting, orgA); affected != 0 {
		t.Fatalf("an UPDATE bound to the retired %s changed %d rows of ANOTHER organisation, want 0", hostTenantSetting, affected)
	}

	// The down half closes the window, restoring the catalog it found. That
	// text is also the later migration that closes it for good, so this is the
	// only place that proves it before it is cut.
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
	if closed := rowSecurityCatalog(t, db); closed != before {
		t.Fatalf("the down half did not restore version 65's catalog:\n%s", firstDifference(before, closed))
	}
	if err := runner.Steps(1); err != nil {
		t.Fatal(err)
	}
	if replayed := rowSecurityCatalog(t, db); replayed != after {
		t.Fatalf("replaying the up half did not reproduce the window:\n%s", firstDifference(after, replayed))
	}
	// Applied twice over, it is the same catalog: the rewrite recognises a
	// predicate already in the window instead of nesting a second COALESCE
	// around it. A migration that is not idempotent here cannot be re-run
	// against a database whose version row was lost.
	if err := applyScopeSettingsTransitionAgain(t, databaseURL); err != nil {
		t.Fatal(err)
	}
	if again := rowSecurityCatalog(t, db); again != after {
		t.Fatalf("applying the up half a second time changed the catalog:\n%s", firstDifference(after, again))
	}
}

// organizationsVisibleWith is every organization id a request transaction sees
// with each named setting bound to the value after it: as app_tenant, which
// holds no BYPASSRLS, in a transaction of its own so no binding outlives it.
//
// It takes a list rather than one setting (scopedOrganizationIDs, which the
// migration-65 test uses) because the question here is what a transaction
// carrying BOTH names sees, which is what the host now binds.
func organizationsVisibleWith(t *testing.T, db *sql.DB, bindings ...string) string {
	t.Helper()
	if len(bindings)%2 != 0 {
		t.Fatalf("bindings must be setting/value pairs, got %d values", len(bindings))
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SET LOCAL ROLE app_tenant`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(bindings); i += 2 {
		if _, err := tx.Exec(`SELECT pg_catalog.set_config($1, $2, true)`, bindings[i], bindings[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	var seen string
	if err := tx.QueryRow(`SELECT coalesce(string_agg(id::text, ',' ORDER BY id::text), '') FROM public.organizations`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	return seen
}

// organizationsRenamedWith is how many rows an UPDATE of organization `target`
// changes under those bindings, as app_tenant, rolled back. The FOR ALL policy's
// WITH CHECK judges the new row, which no SELECT reaches.
func organizationsRenamedWith(t *testing.T, db *sql.DB, target string, bindings ...string) int64 {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SET LOCAL ROLE app_tenant`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(bindings); i += 2 {
		if _, err := tx.Exec(`SELECT pg_catalog.set_config($1, $2, true)`, bindings[i], bindings[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	result, err := tx.Exec(`UPDATE public.organizations SET name = name || '-renamed' WHERE id = $1::uuid`, target)
	if err != nil {
		t.Fatal(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	return affected
}

// applyScopeSettingsTransitionAgain runs migration 66's up half once more,
// outside the runner, in one transaction — the shape an operator gets from
// `psql --single-transaction -f`, and the shape a consumer runs the recipe in.
func applyScopeSettingsTransitionAgain(t *testing.T, databaseURL string) error {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "migrations", "66_scope_settings_transition.up.sql"))
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(string(body)); err != nil {
		return err
	}
	return tx.Commit()
}
