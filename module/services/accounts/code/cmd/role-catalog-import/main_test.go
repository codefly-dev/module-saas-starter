package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
	"accounts/pkg/rolecatalog"
)

func TestReportExitCodes(t *testing.T) {
	nonEmpty := &rolecatalog.Plan{Creates: []rolecatalog.RoleCreate{{Role: rolecatalog.Role{Name: "a"}}}}

	cases := []struct {
		name       string
		result     *infra.ImportResult
		dryRun     bool
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "dry run",
			result:     &infra.ImportResult{Plan: nonEmpty},
			dryRun:     true,
			wantCode:   0,
			wantStdout: "dry-run: no changes applied",
		},
		{
			name:       "refused surfaces reason on stderr with code 2",
			result:     &infra.ImportResult{Plan: nonEmpty, Refused: true, RefusalReason: "would wipe everything"},
			wantCode:   2,
			wantStderr: "would wipe everything",
		},
		{
			name:       "applied no-op",
			result:     &infra.ImportResult{Plan: &rolecatalog.Plan{}, Applied: true},
			wantCode:   0,
			wantStdout: "no changes",
		},
		{
			name:       "applied with changes",
			result:     &infra.ImportResult{Plan: nonEmpty, Applied: true},
			wantCode:   0,
			wantStdout: "applied",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := report(&stdout, &stderr, tc.result, tc.dryRun)
			require.Equal(t, tc.wantCode, code)
			if tc.wantStdout != "" {
				require.Contains(t, stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" {
				require.Contains(t, stderr.String(), tc.wantStderr)
			}
		})
	}
}

func TestRunRequiresFlags(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	var stderr bytes.Buffer
	require.Equal(t, 1, run([]string{"-database-url", "postgres://x"}, &bytes.Buffer{}, &stderr))
	require.Contains(t, stderr.String(), "-catalog is required")

	stderr.Reset()
	require.Equal(t, 1, run([]string{"-catalog", "roles.json"}, &bytes.Buffer{}, &stderr))
	require.Contains(t, stderr.String(), "-database-url")

	stderr.Reset()
	require.Equal(t, 2, run([]string{"-nonexistent-flag"}, &bytes.Buffer{}, &stderr))
	require.True(t, strings.Contains(stderr.String(), "flag provided but not defined") || stderr.Len() > 0)
}

// unsetEnv removes name for the length of the test.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	require.NoError(t, os.Unsetenv(name))
}

func writeCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roles.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"roles":[]}`), 0o600))
	return path
}

// The import records audit events, and where they go depends on the
// deployment's AUDIT_SINK. A deploy step that lost the variable must stop, not
// guess postgres: on a warehouse deployment its events would land in
// audit_events, which nothing reads there.
func TestRunRefusesAnAuditSinkThatIsNotSet(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	catalog := writeCatalog(t)
	// A database nothing listens on: reaching it means the sink check was passed.
	database := "postgres://nobody@127.0.0.1:1/none?connect_timeout=1"

	unsetEnv(t, "AUDIT_SINK")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 1, run([]string{"-catalog", catalog, "-database-url", database}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK is not set")

	t.Setenv("AUDIT_SINK", " ")
	stderr.Reset()
	require.Equal(t, 1, run([]string{"-catalog", catalog, "-database-url", database}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK is not set", "a blank value is as good as none")

	t.Setenv("AUDIT_SINK", "kafka")
	stderr.Reset()
	require.Equal(t, 1, run([]string{"-catalog", catalog, "-database-url", database}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK must be", "a set value is read by the service's own rules")
}

func TestRunTakesTheAuditSinkFromTheEnvironmentOrTheFlag(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	catalog := writeCatalog(t)
	database := "postgres://nobody@127.0.0.1:1/none?connect_timeout=1"

	t.Setenv("AUDIT_SINK", "bigquery")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 1, run([]string{"-catalog", catalog, "-database-url", database}, &stdout, &stderr))
	require.NotContains(t, stderr.String(), "AUDIT_SINK", "a sink in the environment gets past the check, to the database")

	unsetEnv(t, "AUDIT_SINK")
	stderr.Reset()
	require.Equal(t, 1, run([]string{"-catalog", catalog, "-database-url", database, "-audit-sink", "postgres"}, &stdout, &stderr))
	require.NotContains(t, stderr.String(), "AUDIT_SINK", "and so does the flag")

	stderr.Reset()
	require.Equal(t, 1, run([]string{"-catalog", catalog, "-database-url", database, "-audit-sink", ""}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK is not set", "a flag given empty is no sink either")
}
