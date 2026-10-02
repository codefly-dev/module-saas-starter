//go:build !pure

package infra_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Every SECURITY DEFINER function in public lists pg_temp last in its
// search_path. PostgreSQL searches the calling session's temporary schema first
// for relation and type names unless the search_path names pg_temp, so without
// it a caller's temporary table can stand in for a relation the function reads
// with its owner's authority. The rls-migration-gate holds the migrations to the
// same rule; this holds the installed catalog to it, whatever path wrote it.
func TestSecurityDefinerFunctionsListPgTempLast(t *testing.T) {
	rows, err := testPool.Query(testCtx, `
		SELECT proc.oid::regprocedure::text,
		       COALESCE((SELECT setting FROM unnest(proc.proconfig) AS setting
		                  WHERE setting LIKE 'search_path=%'), '')
		  FROM pg_proc proc
		  JOIN pg_namespace namespace ON namespace.oid = proc.pronamespace
		 WHERE namespace.nspname = 'public' AND proc.prosecdef
		 ORDER BY 1`)
	require.NoError(t, err)
	type definer struct{ signature, setting string }
	definers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (definer, error) {
		var d definer
		return d, row.Scan(&d.signature, &d.setting)
	})
	require.NoError(t, err)
	require.NotEmpty(t, definers, "the store defines SECURITY DEFINER functions; an empty inventory would prove nothing")

	var unpinned []string
	for _, d := range definers {
		// proconfig stores the value as PostgreSQL flattened it: the schemas
		// joined by ", ", a quoted name keeping its quotes.
		path := strings.Split(strings.TrimPrefix(d.setting, "search_path="), ", ")
		if d.setting == "" || path[len(path)-1] != "pg_temp" || slices.Contains(path[:len(path)-1], "pg_temp") {
			unpinned = append(unpinned, d.signature+" ["+d.setting+"]")
		}
	}
	require.Empty(t, unpinned, "SECURITY DEFINER functions whose search_path does not list pg_temp last")
}
