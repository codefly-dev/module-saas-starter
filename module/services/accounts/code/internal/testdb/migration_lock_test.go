package testdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These ids were produced by golang-migrate v4.19.1's own
// database.GenerateAdvisoryLockId, the function the store's runner computes its
// lock from. This test pins the derivation against those values; it cannot see a
// change in the runner's own derivation, so the store module — which owns the
// golang-migrate dependency and the connection URL — pins the same values
// against the live function in TestMigrationRunnerAdvisoryLockIDsAreStable.
// Both halves have to agree for the replay to hold a lock the runner respects.
func TestMigrationRunnerLockIDMatchesTheRunnersOwnDerivation(t *testing.T) {
	id, err := MigrationRunnerLockID("postgres://user:password@127.0.0.1:5432/users?sslmode=disable", "public", "users")
	require.NoError(t, err)
	require.EqualValues(t, 2847803293, id)

	other, err := MigrationRunnerLockID("postgres://postgres:example@127.0.0.1:32770/postgres?sslmode=disable", "public", "postgres")
	require.NoError(t, err)
	require.EqualValues(t, 3280485947, other)
}

// The driver takes the database name from the URL path, so the leading slash is
// part of what it hashes. Dropping it yields a different lock.
func TestMigrationRunnerLockIDHashesTheDatabasePath(t *testing.T) {
	id, err := MigrationRunnerLockID("postgres://user:password@127.0.0.1:5432/users?sslmode=disable", "public", "users")
	require.NoError(t, err)
	require.NotEqualValues(t, 3006603353, id, "the bare database name is not what the runner hashes")
}

// A URL with no path leaves the driver's database name empty, and it then reads
// CURRENT_DATABASE() — which carries no slash. Mirroring that fallback is what
// keeps the two in agreement for a connection string of that shape.
func TestMigrationRunnerLockIDFallsBackToTheCurrentDatabase(t *testing.T) {
	id, err := MigrationRunnerLockID("postgres://user:password@127.0.0.1:5432", "public", "users")
	require.NoError(t, err)
	require.EqualValues(t, 3006603353, id)
}

// url.Parse accepts a keyword/value DSN and reports the whole string as the
// path, which would hash to an id nothing holds. Refuse it instead: a lock taken
// against nobody serializes nothing while every test still passes.
func TestMigrationRunnerLockIDRefusesANonURLConnectionString(t *testing.T) {
	_, err := MigrationRunnerLockID("host=127.0.0.1 port=5432 dbname=users user=owner", "public", "users")
	require.ErrorContains(t, err, "not a URL")
}

// Taking the lock twice from one process opens a second session that waits on
// the first, so without this slot the second acquisition blocks until the wait
// budget expires and then blames the store.
func TestMigrationLockSlotRefusesReentry(t *testing.T) {
	require.NoError(t, EnterMigrationLock())
	require.ErrorContains(t, EnterMigrationLock(), "not reentrant")
	ExitMigrationLock()
	require.NoError(t, EnterMigrationLock())
	ExitMigrationLock()
}
