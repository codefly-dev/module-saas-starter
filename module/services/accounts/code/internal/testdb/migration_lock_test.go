package testdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The ids below come from golang-migrate v4.19.1's own
// database.GenerateAdvisoryLockId, the function the store's runner computes its
// lock from. Agreeing with it is the whole point: an id derived even slightly
// differently is a lock nothing else holds, and the replay would be serialized
// against nobody while still reporting success. A toolchain bump that changes
// the derivation has to be noticed here rather than in a queue build.
func TestMigrationRunnerLockIDMatchesTheRunnersOwnDerivation(t *testing.T) {
	id, err := MigrationRunnerLockID("postgres://user:password@127.0.0.1:5432/users?sslmode=disable", "public")
	require.NoError(t, err)
	require.EqualValues(t, 2847803293, id)

	other, err := MigrationRunnerLockID("postgres://postgres:example@127.0.0.1:32770/postgres?sslmode=disable", "public")
	require.NoError(t, err)
	require.EqualValues(t, 3280485947, other)
}

// The driver takes the database name from the URL path, so the leading slash is
// part of what it hashes. Dropping it yields a different lock.
func TestMigrationRunnerLockIDHashesTheDatabasePath(t *testing.T) {
	id, err := MigrationRunnerLockID("postgres://user:password@127.0.0.1:5432/users?sslmode=disable", "public")
	require.NoError(t, err)
	require.NotEqualValues(t, 3006603353, id, "the bare database name is not what the runner hashes")
}
