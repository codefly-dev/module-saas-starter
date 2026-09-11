package main

import (
	"database/sql"
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
)

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
	proposed, err := filepath.Abs("../migrations")
	if err != nil {
		t.Fatal(err)
	}
	// A test-only migration exercises execution evidence even on runner-only changes.
	files, err := os.ReadDir(proposed)
	if err != nil {
		t.Fatal(err)
	}
	probeTree := t.TempDir()
	maximum := 0
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(proposed, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(probeTree, file.Name()), body, 0600); err != nil {
			t.Fatal(err)
		}
		version, err := strconv.Atoi(strings.SplitN(file.Name(), "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		if version > maximum {
			maximum = version
		}
	}
	for direction, body := range map[string]string{
		"up":   "CREATE TABLE migration_probe (user_id uuid PRIMARY KEY REFERENCES users(uuid)); INSERT INTO migration_probe SELECT uuid FROM users;",
		"down": "DROP TABLE migration_probe;",
	} {
		if err := os.WriteFile(filepath.Join(probeTree, fmt.Sprintf("%d_probe.%s.sql", maximum+1, direction)), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	proposed = probeTree
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
	apply(proposed, upgradeURL)
	entries, err := os.ReadDir(proposed)
	if err != nil {
		t.Fatal(err)
	}
	var expected []int
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		version, err := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		if version > frontier {
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
	apply(proposed, cleanURL)
	dump := func(id string) string {
		raw := command("exec", id, "pg_dump", "-U", "postgres", "--schema-only", "postgres")
		// pg_dump adds a random psql restriction token unrelated to the schema.
		return regexp.MustCompile(`(?m)^\\(?:un)?restrict .*\n?`).ReplaceAllString(raw, "")
	}
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
}
