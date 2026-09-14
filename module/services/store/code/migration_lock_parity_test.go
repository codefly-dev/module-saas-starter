package main

import (
	"testing"

	"github.com/golang-migrate/migrate/v4/database"
)

// The accounts migration replays (internal/testdb.MigrationRunnerLockID) take
// the advisory lock this service's runner holds for the length of a migration
// run, so that a replay and a starting store cannot rewrite the same catalog
// rows at once. They recompute the id rather than call this function, because
// they do not depend on golang-migrate — which leaves the two derivations free
// to drift apart silently: a replay holding an id this runner never takes
// serializes against nothing and still reports success.
//
// This test is the half that can see the runner. It fails if golang-migrate
// changes its derivation, and it is where to look if this service's connection
// URL ever grows an x-migrations-table override — that moves the id too, with
// no change to the library at all. The values are the ones accounts pins in
// TestMigrationRunnerLockIDMatchesTheRunnersOwnDerivation.
func TestMigrationRunnerAdvisoryLockIDsAreStable(t *testing.T) {
	for _, testCase := range []struct {
		database string
		schema   string
		table    string
		want     string
	}{
		// The database name as the driver reads it off the URL path, which is
		// how the store is configured (service.codefly.yaml, database-name: users).
		{"/users", "public", "schema_migrations", "2847803293"},
		// The CURRENT_DATABASE() fallback, for a URL that carries no path.
		{"users", "public", "schema_migrations", "3006603353"},
	} {
		got, err := database.GenerateAdvisoryLockId(testCase.database, testCase.schema, testCase.table)
		if err != nil {
			t.Fatalf("generate advisory lock id for %q: %v", testCase.database, err)
		}
		if got != testCase.want {
			t.Fatalf("advisory lock id for database %q, schema %q, table %q: got %s, want %s — "+
				"the accounts migration replay pins %s and would now lock against nothing",
				testCase.database, testCase.schema, testCase.table, got, testCase.want, testCase.want)
		}
	}
}
