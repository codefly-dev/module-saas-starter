package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
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
