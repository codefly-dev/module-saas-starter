package rolecatalogimport

import (
	"bytes"
	"context"
	"errors"
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
	unsetEnv(t, "AUDIT_SINK")

	var stderr bytes.Buffer
	require.Equal(t, 1, Run([]string{"-database-url", "postgres://x"}, &bytes.Buffer{}, &stderr))
	require.Contains(t, stderr.String(), "-catalog is required")

	stderr.Reset()
	require.Equal(t, 1, Run([]string{"-catalog", "roles.json"}, &bytes.Buffer{}, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK is not set", "without an explicit URL the deployed connection is selected after checking the sink")

	stderr.Reset()
	require.Equal(t, 2, Run([]string{"-nonexistent-flag"}, &bytes.Buffer{}, &stderr))
	require.True(t, strings.Contains(stderr.String(), "flag provided but not defined") || stderr.Len() > 0)
}

func TestRunSelectsDeployedCredentialsUnlessAnExplicitURLWasSupplied(t *testing.T) {
	catalog := writeCatalog(t)
	t.Setenv("AUDIT_SINK", "postgres")
	for _, tc := range []struct {
		name, environment, flag, wantURL, mode string
	}{
		{name: "deployed capabilities"},
		{name: "warehouse mode with deployed capabilities", mode: "bigquery"},
		{name: "environment operator URL", environment: "postgres://operator@example.invalid/db", wantURL: "postgres://operator@example.invalid/db"},
		{name: "flag wins over environment", environment: "postgres://ignored@example.invalid/db", flag: "postgres://selected@example.invalid/db", wantURL: "postgres://selected@example.invalid/db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tc.environment)
			mode := tc.mode
			if mode == "" {
				mode = "postgres"
			}
			t.Setenv("AUDIT_SINK", mode)
			deployed, explicit := 0, 0
			var selectedURL string
			openers := storeOpeners{
				deployed: func(context.Context) (*infra.PostgresStore, error) {
					deployed++
					return nil, errors.New("stop before database connection")
				},
				explicit: func(_ context.Context, url string) (*infra.PostgresStore, error) {
					explicit++
					selectedURL = url
					return nil, errors.New("stop before database connection")
				},
			}
			args := []string{"-catalog", catalog}
			if tc.flag != "" {
				args = append(args, "-database-url", tc.flag)
			}
			var stderr bytes.Buffer
			require.Equal(t, 1, runWithOpeners(args, &bytes.Buffer{}, &stderr, openers))
			require.Contains(t, stderr.String(), "stop before database connection")
			if tc.wantURL == "" {
				require.Equal(t, 1, deployed)
				require.Zero(t, explicit)
			} else {
				require.Zero(t, deployed)
				require.Equal(t, 1, explicit)
				require.Equal(t, tc.wantURL, selectedURL)
			}
		})
	}
}

func TestRunRefusesABlankExplicitURLInsteadOfSwitchingToManaged(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://operator@example.invalid/db")
	t.Setenv("AUDIT_SINK", "postgres")
	var stderr bytes.Buffer
	require.Equal(t, 1, Run([]string{"-catalog", writeCatalog(t), "-database-url", " "}, &bytes.Buffer{}, &stderr))
	require.Contains(t, stderr.String(), "an explicit -database-url must not be blank")
}

func TestRunChecksAuditModeBeforeResolvingManagedCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	catalog := writeCatalog(t)
	openers := storeOpeners{
		deployed: func(context.Context) (*infra.PostgresStore, error) {
			t.Fatal("must not resolve credentials without an explicit audit mode")
			return nil, nil
		},
		explicit: func(context.Context, string) (*infra.PostgresStore, error) {
			t.Fatal("must not connect without an explicit audit mode")
			return nil, nil
		},
	}
	for _, mode := range []string{"", " ", "unsupported"} {
		t.Setenv("AUDIT_SINK", mode)
		var stderr bytes.Buffer
		require.Equal(t, 1, runWithOpeners([]string{"-catalog", catalog}, &bytes.Buffer{}, &stderr, openers))
		require.Contains(t, stderr.String(), "AUDIT_SINK")
	}
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
	require.Equal(t, 1, Run([]string{"-catalog", catalog, "-database-url", database}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK is not set")

	t.Setenv("AUDIT_SINK", " ")
	stderr.Reset()
	require.Equal(t, 1, Run([]string{"-catalog", catalog, "-database-url", database}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK is not set", "a blank value is as good as none")

	t.Setenv("AUDIT_SINK", "kafka")
	stderr.Reset()
	require.Equal(t, 1, Run([]string{"-catalog", catalog, "-database-url", database}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK must be", "a set value is read by the service's own rules")
}

func TestRunTakesTheAuditSinkFromTheEnvironmentOrTheFlag(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	catalog := writeCatalog(t)
	database := "postgres://nobody@127.0.0.1:1/none?connect_timeout=1"

	t.Setenv("AUDIT_SINK", "bigquery")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 1, Run([]string{"-catalog", catalog, "-database-url", database}, &stdout, &stderr))
	require.NotContains(t, stderr.String(), "AUDIT_SINK", "a sink in the environment gets past the check, to the database")

	unsetEnv(t, "AUDIT_SINK")
	stderr.Reset()
	require.Equal(t, 1, Run([]string{"-catalog", catalog, "-database-url", database, "-audit-sink", "postgres"}, &stdout, &stderr))
	require.NotContains(t, stderr.String(), "AUDIT_SINK", "and so does the flag")

	stderr.Reset()
	require.Equal(t, 1, Run([]string{"-catalog", catalog, "-database-url", database, "-audit-sink", ""}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "AUDIT_SINK is not set", "a flag given empty is no sink either")
}
