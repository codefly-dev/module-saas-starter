package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// A database built from a ledger whose numbers later moved holds a version the
// current ledger also has, under another migration's name. The runner records
// every applied migration's name and refuses such a database by name rather
// than skipping the current ledger's migration at that number and failing
// dirty further on; a ledger that only adds migrations upgrades as before.
func TestMigrationLedgerWhoseNamesMovedIsRefused(t *testing.T) {
	if os.Getenv("MIGRATION_CONSENT_REPLAY") == "" {
		t.Skip("set MIGRATION_CONSENT_REPLAY=1 (migration-reference-gate.mjs --replay does): starts a throwaway PostgreSQL cluster")
	}
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	started := docker("run", "--rm", "-d", "-e", "POSTGRES_PASSWORD=example", "-p", "127.0.0.1::5432", "postgres:16")
	id := started[strings.LastIndex(started, "\n")+1:]
	t.Cleanup(func() { docker("rm", "-f", id) })
	port := strings.TrimPrefix(docker("port", id, "5432/tcp"), "127.0.0.1:")
	url := "postgres://postgres:example@127.0.0.1:" + port + "/postgres?sslmode=disable"
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i := 0; ; i++ {
		if err = db.Ping(); err == nil {
			break
		}
		if i > 120 {
			t.Fatalf("postgres did not come up: %v", err)
		}
		exec.Command("sleep", "0.5").Run()
	}
	ledger := func(files map[string]string) string {
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return "file://" + dir
	}
	first := map[string]string{
		"1_one.up.sql": "CREATE TABLE one (id int);", "1_one.down.sql": "DROP TABLE one;",
		"2_two.up.sql": "CREATE TABLE two (id int);", "2_two.down.sql": "DROP TABLE two;",
	}
	if err := migrateStoreFrom(ledger(first), url); err != nil {
		t.Fatalf("install the first ledger: %v", err)
	}
	var comment string
	if err := db.QueryRow(`SELECT obj_description('public.schema_migrations'::regclass, 'pg_class')`).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(comment, `"2":"two"`) {
		t.Fatalf("the applied names are not recorded: %q", comment)
	}

	moved := map[string]string{
		"1_one.up.sql": first["1_one.up.sql"], "1_one.down.sql": first["1_one.down.sql"],
		"2_retention.up.sql": "CREATE TABLE retention (id int);", "2_retention.down.sql": "DROP TABLE retention;",
		"3_two.up.sql": first["2_two.up.sql"], "3_two.down.sql": first["2_two.down.sql"],
	}
	err = migrateStoreFrom(ledger(moved), url)
	if err == nil || !strings.Contains(err.Error(), `applied migration 2 as "two"`) || !strings.Contains(err.Error(), `"retention"`) {
		t.Fatalf("a ledger whose numbers moved must be refused by name: %v", err)
	}

	grown := map[string]string{"3_three.up.sql": "CREATE TABLE three (id int);", "3_three.down.sql": "DROP TABLE three;"}
	for name, body := range first {
		grown[name] = body
	}
	if err := migrateStoreFrom(ledger(grown), url); err != nil {
		t.Fatalf("a ledger that only adds migrations upgrades: %v", err)
	}
	if err := db.QueryRow(`SELECT obj_description('public.schema_migrations'::regclass, 'pg_class')`).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(comment, `"3":"three"`) {
		t.Fatalf("the upgrade's names are not recorded: %q", comment)
	}
	// A pre-renumber database has no name stamp. It cannot be diagnosed by
	// that guard; when replay fails, the refusal must name the recovery action.
	if _, err := db.Exec(`COMMENT ON TABLE public.schema_migrations IS NULL`); err != nil {
		t.Fatal(err)
	}
	grown["4_conflict.up.sql"] = "CREATE TABLE two (id int);"
	grown["4_conflict.down.sql"] = "DROP TABLE two;"
	err = migrateStoreFrom(ledger(grown), url)
	if err == nil || !strings.Contains(err.Error(), "before migrations 46–49 were renumbered to 49–52 must be recreated") {
		t.Fatalf("an unstamped pre-renumber database needs a recreation remedy: %v", err)
	}

}
