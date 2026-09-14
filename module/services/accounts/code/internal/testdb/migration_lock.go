package testdb

import (
	"fmt"
	"hash/crc32"
	"net/url"
	"strings"
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
// The runner hashes the connection URL's path, leading slash included: it reads
// the database name off the parsed URL rather than from CURRENT_DATABASE(), so
// the id is derived from "/users", not "users". Pass the schema the connection
// resolves CURRENT_SCHEMA() to, which is what the runner records for its own.
func MigrationRunnerLockID(connectionString, schema string) (int64, error) {
	parsed, err := url.Parse(connectionString)
	if err != nil {
		return 0, fmt.Errorf("parse store connection string: %w", err)
	}
	name := strings.Join([]string{schema, migrationsTableName, parsed.Path}, "\x00")
	return int64(crc32.ChecksumIEEE([]byte(name)) * advisoryLockIDSalt), nil
}
