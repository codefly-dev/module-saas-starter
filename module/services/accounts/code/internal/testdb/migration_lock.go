package testdb

import (
	"errors"
	"fmt"
	"hash/crc32"
	"net/url"
	"strings"
	"sync/atomic"
)

const (
	// The salt and default version table golang-migrate's postgres driver
	// derives its advisory-lock id from (database.GenerateAdvisoryLockId,
	// DefaultMigrationsTable). The store runs the driver on its defaults.
	advisoryLockIDSalt  uint32 = 1486364155
	migrationsTableName        = "schema_migrations"
)

// MigrationRunnerLockID returns the advisory-lock id the store's migration
// runner holds for the length of a migration run. A test that replays a
// migration by hand takes the same lock, so that a store instance still
// applying migrations cannot rewrite the same catalog rows at the same moment.
//
// The runner hashes the connection URL's path, leading slash included — it reads
// the database name off the parsed URL, so the id is derived from "/users", not
// "users" — and falls back to the session's current database only when the URL
// carries no path at all. currentDatabase supplies that fallback; schema is what
// the connection resolves CURRENT_SCHEMA() to, which is what the runner records
// for its own.
//
// A connection string that is not a URL is refused rather than hashed. url.Parse
// accepts a keyword/value DSN ("host=… dbname=…") without complaint and yields
// the whole string as the path, which would hash to an id no runner holds — a
// lock against nobody that every test still reports as success.
func MigrationRunnerLockID(connectionString, schema, currentDatabase string) (int64, error) {
	parsed, err := url.Parse(connectionString)
	if err != nil {
		return 0, fmt.Errorf("parse store connection string: %w", err)
	}
	if parsed.Scheme == "" {
		return 0, errors.New("store connection string is not a URL: the migration runner parses it as one")
	}
	database := parsed.Path
	if database == "" {
		database = currentDatabase
	}
	if database == "" {
		return 0, errors.New("store connection string names no database")
	}
	name := strings.Join([]string{schema, migrationsTableName, database}, "\x00")
	return int64(crc32.ChecksumIEEE([]byte(name)) * advisoryLockIDSalt), nil
}

var migrationLockHeld atomic.Bool

// EnterMigrationLock claims this process's single migration-lock slot, and
// ExitMigrationLock returns it. The database lock they guard is exclusive per
// session, so taking it a second time from the same process opens a second
// session that waits on the first until the wait budget runs out — a self
// deadlock that surfaces as a timeout accusing the store. Claiming the slot
// turns that into an immediate failure naming the real cause.
func EnterMigrationLock() error {
	if !migrationLockHeld.CompareAndSwap(false, true) {
		return errors.New("migration lock already held by this process: the helper holding it is not reentrant")
	}
	return nil
}

func ExitMigrationLock() {
	migrationLockHeld.Store(false)
}
