package main

import (
	"bytes"
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
