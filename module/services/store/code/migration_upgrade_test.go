package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestMigrationUpgrade proves, against real PostgreSQL clusters, that the
// proposed ledger installs the schema the reference ledger describes.
//
// Two shapes of change reach it. A forward migration is proved as an upgrade:
// the reference ledger is installed, representative tenant rows are written,
// the proposed ledger is applied on top, and the result must equal a clean
// install of the proposed ledger with nothing lost. A fold — the whole ledger
// regenerated into one baseline, sharing no version with the reference — has
// no upgrade path by design, so it is proved as equivalence instead: a clean
// install of the reference ledger and a clean install of the proposed one must
// dump to the same schema and carry the same seed rows. Either way the runner
// itself is exercised, and a database installed by a ledger the proposed source
// does not contain must be refused rather than silently left behind.
func TestMigrationUpgrade(t *testing.T) {
	reference := os.Getenv("MIGRATION_REFERENCE")
	if reference == "" {
		t.Skip("run through migration-reference-gate.mjs <reference> --replay")
	}
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// `docker run -d` prints image-pull progress before the container id, so fetch the
	// image up front and keep only the last line, which is the id on any runner.
	command("pull", "postgres:16")
	// Separate clusters keep global roles and default grants from contaminating the clean install.
	database := func() (*sql.DB, string, string) {
		started := command("run", "--rm", "-d", "-e", "POSTGRES_PASSWORD=example", "-p", "127.0.0.1::5432", "postgres:16")
		id := started[strings.LastIndex(started, "\n")+1:]
		t.Cleanup(func() { command("rm", "-f", id) })
		port := strings.TrimPrefix(command("port", id, "5432/tcp"), "127.0.0.1:")
		url := "postgres://postgres:example@127.0.0.1:" + port + "/postgres?sslmode=disable"
		db, err := sql.Open("postgres", url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		deadline := time.Now().Add(30 * time.Second)
		for db.Ping() != nil {
			if time.Now().After(deadline) {
				t.Fatal("postgres did not become ready")
			}
			time.Sleep(200 * time.Millisecond)
		}
		return db, url, id
	}
	apply := func(dir, url string) {
		t.Helper()
		if err := migrateStoreFrom("file://"+dir, url); err != nil {
			t.Fatal(err)
		}
	}
	execute := func(db *sql.DB, query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	dump := func(id string) string {
		raw := command("exec", id, "pg_dump", "-U", "postgres", "--schema-only", "postgres")
		// pg_dump adds a random psql restriction token unrelated to the schema.
		return regexp.MustCompile(`(?m)^\\(?:un)?restrict .*\n?`).ReplaceAllString(raw, "")
	}
	proposed, err := filepath.Abs("../migrations")
	if err != nil {
		t.Fatal(err)
	}

	// A test-only migration exercises execution evidence even on runner-only changes.
	probeTree := t.TempDir()
	maximum := 0
	for _, file := range listSQL(t, proposed) {
		body, err := os.ReadFile(filepath.Join(proposed, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(probeTree, file), body, 0600); err != nil {
			t.Fatal(err)
		}
		if version := versionOf(t, file); version > maximum {
			maximum = version
		}
	}
	for direction, body := range map[string]string{
		"up":   "CREATE TABLE public.migration_probe (user_id uuid PRIMARY KEY REFERENCES public.users(uuid)); INSERT INTO public.migration_probe SELECT uuid FROM public.users;",
		"down": "DROP TABLE public.migration_probe;",
	} {
		if err := os.WriteFile(filepath.Join(probeTree, fmt.Sprintf("%d_probe.%s.sql", maximum+1, direction)), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// A forward migration leaves every reference file in place, byte for byte. A
	// fold keeps none of them: the regenerated baseline may reuse a version number
	// (a fold of a folded ledger rewrites 1_baseline itself), so identity is the
	// file's content, never its number.
	fold := true
	for _, file := range listSQL(t, reference) {
		before, err := os.ReadFile(filepath.Join(reference, file))
		if err != nil {
			t.Fatal(err)
		}
		if after, err := os.ReadFile(filepath.Join(proposed, file)); err == nil && string(after) == string(before) {
			fold = false
		}
	}

	if fold {
		// The reference ledger is folded away entirely. The fold itself — the lowest
		// version in the proposal — must install exactly what the reference
		// installed; whatever the proposal adds above it is then an ordinary
		// upgrade from that baseline, and must equal a clean install of the whole.
		lowest := 0
		for _, file := range listSQL(t, proposed) {
			if version := versionOf(t, file); lowest == 0 || version < lowest {
				lowest = version
			}
		}
		baselineTree := t.TempDir()
		for _, file := range listSQL(t, proposed) {
			if versionOf(t, file) != lowest {
				continue
			}
			body, err := os.ReadFile(filepath.Join(proposed, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(baselineTree, file), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		referenceDB, referenceURL, referenceID := database()
		apply(reference, referenceURL)
		baselineDB, baselineURL, baselineID := database()
		apply(baselineTree, baselineURL)
		if a, b := dump(referenceID), dump(baselineID); a != b {
			t.Fatalf("folded baseline installs a different schema than the ledger it replaced (tables, functions, policies, or grants)\n%s", firstDifference(a, b))
		}
		if a, b := seedCatalog(t, referenceDB), seedCatalog(t, baselineDB); a != b {
			t.Fatalf("folded baseline carries different seed rows than the ledger it replaced:\n%s", firstDifference(a, b))
		}
		// Role attributes are the one deliberate difference from a legacy ledger:
		// the baseline creates every runtime role without BYPASSRLS, the shape the
		// explicit background policies were written for, and a fold must not bring
		// the legacy attribute back.
		var bypassing int
		if err := baselineDB.QueryRow("SELECT count(*) FROM pg_roles WHERE rolname LIKE 'app_%' AND (rolbypassrls OR rolsuper OR rolcanlogin)").Scan(&bypassing); err != nil {
			t.Fatal(err)
		}
		if bypassing != 0 {
			t.Fatalf("%d runtime role(s) installed with BYPASSRLS, SUPERUSER or LOGIN", bypassing)
		}
		t.Logf("verified: baseline %d is schema- and seed-equivalent to the reference ledger, with NOBYPASSRLS runtime roles", lowest)
		// Everything above the fold, plus the probe, upgrades the baseline to the
		// same schema a clean install of the whole proposal produces.
		apply(probeTree, baselineURL)
		_, cleanURL, cleanID := database()
		apply(probeTree, cleanURL)
		if dump(baselineID) != dump(cleanID) {
			t.Fatal("migrations above the baseline upgrade it to a schema that differs from a clean install")
		}
		var probed int
		if err := baselineDB.QueryRow("SELECT count(*) FROM migration_probe").Scan(&probed); err != nil {
			t.Fatal(err)
		}
		t.Logf("verified versions above %d through %d on top of the baseline", lowest, maximum+1)
		refuseForeign(t, baselineURL)
		return
	}

	upgraded, upgradeURL, upgradeID := database()
	apply(reference, upgradeURL)
	execute(upgraded, `
 INSERT INTO users(uuid, primary_email, profile) VALUES ('00000000-0000-0000-0000-000000000001', 'user@example.com', '{"name":"Jane Doe"}');
 INSERT INTO organizations(id, name, slug, owner_id) VALUES ('00000000-0000-0000-0000-000000000002', 'Acme', 'example-upgrade', '00000000-0000-0000-0000-000000000001');
 INSERT INTO organization_members(org_id,user_id,role) VALUES ('00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000001','owner');
 INSERT INTO teams(id,org_id,name,slug,path) VALUES ('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000002','Example','example','example');
 CREATE SCHEMA migration_evidence;
 CREATE TABLE migration_evidence.applied(version bigint);
 CREATE FUNCTION migration_evidence.record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT NEW.dirty THEN INSERT INTO migration_evidence.applied VALUES (NEW.version); END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER migration_evidence AFTER INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION migration_evidence.record();`)
	var frontier int
	if err := upgraded.QueryRow("SELECT version FROM schema_migrations WHERE NOT dirty").Scan(&frontier); err != nil {
		t.Fatal(err)
	}
	apply(probeTree, upgradeURL)
	var expected []int
	for _, file := range listSQL(t, probeTree) {
		if !strings.HasSuffix(file, ".up.sql") {
			continue
		}
		if version := versionOf(t, file); version > frontier {
			expected = append(expected, version)
		}
	}
	sort.Ints(expected)
	rows, err := upgraded.Query("SELECT version FROM migration_evidence.applied ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	var actual []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("executed versions %v, want %v", actual, expected)
	}
	t.Logf("verified added versions: %v", actual)
	var preserved bool
	if err := upgraded.QueryRow(`SELECT EXISTS (
 SELECT 1 FROM users u JOIN organizations o ON o.owner_id=u.uuid
 JOIN organization_members m ON m.org_id=o.id AND m.user_id=u.uuid
 JOIN teams t ON t.org_id=o.id
 JOIN migration_probe p ON p.user_id=u.uuid
 WHERE u.primary_email='user@example.com' AND u.profile->>'name'='Jane Doe'
 AND o.slug='example-upgrade' AND m.role='owner' AND t.name='Example')`).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if !preserved {
		t.Fatal("representative tenant data was lost")
	}
	execute(upgraded, "DROP TRIGGER migration_evidence ON schema_migrations; DROP SCHEMA migration_evidence CASCADE;")
	_, cleanURL, cleanID := database()
	apply(probeTree, cleanURL)
	if dump(upgradeID) != dump(cleanID) {
		t.Fatal("upgraded schema differs from clean install (tables, functions, policies, or grants)")
	}
	var dirty bool
	if err := upgraded.QueryRow("SELECT dirty FROM schema_migrations").Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("upgrade left migration version dirty")
	}
	refuseForeign(t, cleanURL)
}

// A database whose recorded version the source does not contain was installed
// by another ledger. golang-migrate would treat it as already ahead and apply
// nothing; the runner has to refuse it instead, and say what to do.
func refuseForeign(t *testing.T, url string) {
	t.Helper()
	foreign := t.TempDir()
	for _, direction := range []string{"up", "down"} {
		if err := os.WriteFile(filepath.Join(foreign, "9000_foreign."+direction+".sql"), []byte("SELECT 1;"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := migrateStoreFrom("file://"+foreign, url)
	if err == nil || !strings.Contains(err.Error(), "recreate the database") {
		t.Fatalf("a database installed by another ledger must be refused, got %v", err)
	}
	t.Log("verified: a database installed by another ledger is refused, not left behind")
}

func listSQL(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	return files
}

func versionOf(t *testing.T, file string) int {
	t.Helper()
	version, err := strconv.Atoi(strings.SplitN(file, "_", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	return version
}

// seedCatalog is the seed data with the install-specific parts removed: the
// generated ids of the natural-key tables are replaced by their keys (so the
// foreign keys that name them compare too), and timestamps are dropped.
func seedCatalog(t *testing.T, db *sql.DB) string {
	t.Helper()
	remap := map[string]string{}
	for _, entry := range []struct{ table, key string }{
		{"roles", "name"}, {"plans", "name"}, {"email_templates", "name"}, {"data_retention_policies", "resource_type"},
	} {
		rows, err := db.Query(fmt.Sprintf("SELECT id::text, %s FROM public.%s", entry.key, entry.table))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, key string
			if err := rows.Scan(&id, &key); err != nil {
				t.Fatal(err)
			}
			remap[id] = "seed:" + entry.table + ":" + key
		}
		rows.Close()
	}
	var out []string
	for _, table := range []string{"audit_event_types", "bootstrap_state", "data_retention_policies", "email_templates", "identity_providers", "plan_entitlements", "plans", "role_permissions", "roles"} {
		var raw string
		if err := db.QueryRow(fmt.Sprintf("SELECT coalesce(json_agg(to_jsonb(t) - 'created_at' - 'updated_at'), '[]')::text FROM public.%s t", table)).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		for id, key := range remap {
			raw = strings.ReplaceAll(raw, id, key)
		}
		var items []map[string]any
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, item := range items {
			canonical, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(canonical))
		}
		sort.Strings(lines)
		out = append(out, table+":\n  "+strings.Join(lines, "\n  "))
	}
	return strings.Join(out, "\n")
}

func firstDifference(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("line %d:\n  reference: %s\n  proposed:  %s", i+1, al[i], bl[i])
		}
	}
	return fmt.Sprintf("lengths differ: %d vs %d lines", len(al), len(bl))
}
