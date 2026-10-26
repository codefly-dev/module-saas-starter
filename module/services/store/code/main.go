package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()
	if _, err := codefly.Init(ctx); err != nil {
		return fmt.Errorf("initialize Codefly SDK: %w", err)
	}
	databaseURL, err := codefly.For(ctx).Service("store").Secret("postgres", "owner-connection")
	if err != nil {
		return fmt.Errorf("resolve store owner connection through Codefly SDK: %w", err)
	}
	return migrateStore(databaseURL)
}

func migrateStore(databaseURL string) error {
	return migrateStoreFrom("file:///app/migrations", databaseURL)
}

func migrateStoreFrom(sourceURL, databaseURL string) error {
	if databaseURL == "" {
		return errors.New("store owner connection is empty")
	}
	runner, err := migrate.New(sourceURL, databaseURL)
	if err != nil {
		return fmt.Errorf("initialize database migrations: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := runner.Close()
		if sourceErr != nil {
			log.Printf("close migration source: %v", sourceErr)
		}
		if databaseErr != nil {
			log.Printf("close migration database: %v", databaseErr)
		}
	}()

	if err := refuseForeignLedger(runner, sourceURL, databaseURL); err != nil {
		return err
	}
	names, err := ledgerNames(sourceURL)
	if err != nil {
		return err
	}
	if err := refuseRenamedLedger(databaseURL, names); err != nil {
		return err
	}
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply database migrations: %w; local and validation databases created before migrations 46–49 were renumbered to 49–52 must be recreated; no upgrade path is supported for those databases", err)
	}
	return recordLedgerNames(runner, databaseURL, names)
}

// The ledger's versions are numbers, and a number can be reused: renumbering
// migrations while databases built from the old numbers exist leaves one at a
// version this source also has, under another migration's name. Up then skips
// the source's migration at that number and fails on a later one, dirty. So the
// runner records the NAME of every migration it applied (a comment on its own
// version table, as the baseline stamps the schema) and refuses a database
// whose recorded names this source contradicts. A database recorded before the
// names were (none on its table) cannot be checked, and is recorded now.
const ledgerNamesPrefix = "codefly store ledger: "

var migrationFile = regexp.MustCompile(`^([0-9]+)_(.+)\.up\.sql$`)

// ledgerNames is each migration of a file source by version, its name being
// the file name between the version and ".up.sql". A source that is not a
// directory names nothing.
func ledgerNames(sourceURL string) (map[string]string, error) {
	dir := strings.TrimPrefix(sourceURL, "file://")
	if parsed, err := url.Parse(sourceURL); err == nil && parsed.Scheme == "file" {
		dir = parsed.Path
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the migration names: %w", err)
	}
	names := map[string]string{}
	for _, entry := range entries {
		if match := migrationFile.FindStringSubmatch(entry.Name()); match != nil {
			names[strings.TrimLeft(match[1], "0")] = match[2]
		}
	}
	return names, nil
}

func refuseRenamedLedger(databaseURL string, names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	recorded, err := readLedgerNames(databaseURL)
	if err != nil || recorded == nil {
		return err
	}
	versions := make([]string, 0, len(recorded))
	for version := range recorded {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	for _, version := range versions {
		if name, ok := names[version]; ok && name != recorded[version] {
			return fmt.Errorf("database applied migration %s as %q and this ledger's migration %s is %q: it was installed by a ledger whose numbers moved, and the store carries no upgrade path; recreate the database", version, recorded[version], version, name)
		}
	}
	return nil
}

func readLedgerNames(databaseURL string) (map[string]string, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database to read the ledger names: %w", err)
	}
	defer db.Close()
	var comment sql.NullString
	err = db.QueryRow(`SELECT obj_description(to_regclass('public.schema_migrations'), 'pg_class')`).Scan(&comment)
	if err != nil {
		return nil, fmt.Errorf("read the ledger names: %w", err)
	}
	body, stamped := strings.CutPrefix(comment.String, ledgerNamesPrefix)
	if !comment.Valid || !stamped {
		return nil, nil
	}
	recorded := map[string]string{}
	if err := json.Unmarshal([]byte(body), &recorded); err != nil {
		return nil, fmt.Errorf("the ledger names on the version table cannot be read: %w", err)
	}
	return recorded, nil
}

// recordLedgerNames stamps the name of every migration at or below the
// database's version.
func recordLedgerNames(runner *migrate.Migrate, databaseURL string, names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	version, _, err := runner.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read database migration version: %w", err)
	}
	applied := map[string]string{}
	for v, name := range names {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n <= uint64(version) {
			applied[v] = name
		}
	}
	body, err := json.Marshal(applied)
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return fmt.Errorf("open database to record the ledger names: %w", err)
	}
	defer db.Close()
	if _, err := db.Exec(`COMMENT ON TABLE public.schema_migrations IS ` + quoteLiteral(ledgerNamesPrefix+string(body))); err != nil {
		return fmt.Errorf("record the ledger names: %w", err)
	}
	return nil
}

// quoteLiteral is a SQL string literal (COMMENT takes no parameter).
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// The ledger is one baseline and carries no upgrade path, so a database whose
// recorded version this source does not contain was installed by an earlier
// ledger. golang-migrate's Up would treat it as already ahead and do nothing,
// leaving the old schema in service under a runner that believes it is current;
// the only correct answer is to refuse until the database is recreated.
func refuseForeignLedger(runner *migrate.Migrate, sourceURL, databaseURL string) error {
	version, dirty, err := runner.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read database migration version: %w", err)
	}
	src, err := source.Open(sourceURL)
	if err != nil {
		return fmt.Errorf("open migration source: %w", err)
	}
	defer src.Close()
	if _, _, err := src.ReadUp(version); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("database is at migration %d (dirty=%t), which this ledger does not contain: it was installed by an earlier ledger and the store carries no upgrade path; recreate the database", version, dirty)
		}
		return fmt.Errorf("read migration %d from source: %w", version, err)
	}
	return refuseForeignBaseline(sourceURL, databaseURL)
}

var baselineID = regexp.MustCompile(`(?m)^-- baseline-id: ([0-9a-f-]{36})$`)

// A regenerated baseline keeps its version number, so version alone cannot tell
// a database installed by the previous baseline from one installed by this. The
// baseline stamps its identity on the schema; a database stamped by another is
// refused like any other foreign ledger.
func refuseForeignBaseline(sourceURL, databaseURL string) error {
	dir := strings.TrimPrefix(sourceURL, "file://")
	if parsed, err := url.Parse(sourceURL); err == nil && parsed.Scheme == "file" {
		dir = parsed.Path
	}
	first, err := filepath.Glob(filepath.Join(dir, "1_*.up.sql"))
	if err != nil || len(first) != 1 {
		return nil
	}
	body, err := os.ReadFile(first[0])
	if err != nil {
		return fmt.Errorf("read baseline: %w", err)
	}
	match := baselineID.FindSubmatch(body)
	if match == nil {
		return nil
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return fmt.Errorf("open database to read the baseline stamp: %w", err)
	}
	defer db.Close()
	var stamp sql.NullString
	if err := db.QueryRow("SELECT obj_description('public'::regnamespace, 'pg_namespace')").Scan(&stamp); err != nil {
		return fmt.Errorf("read the baseline stamp: %w", err)
	}
	want := "codefly store baseline " + string(match[1])
	if !stamp.Valid || stamp.String != want {
		return fmt.Errorf("database was installed by baseline %q, this ledger is %q: a regenerated baseline carries no upgrade path; recreate the database", stamp.String, want)
	}
	return nil
}
