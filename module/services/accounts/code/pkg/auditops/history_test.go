package auditops

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

var swapEnv = map[string]string{
	"DATABASE_URL":                 "postgres://example.invalid/accounts",
	"AUDIT_SINK":                   "bigquery",
	"AUDIT_BIGQUERY_PROJECT":       "example-project",
	"AUDIT_BIGQUERY_DATASET":       "audit",
	"AUDIT_ARCHIVE_URL":            "gs://example-audit-archive",
	"AUDIT_DEPLOYMENT_ID":          "deployment-1",
	"AUDIT_CONTENT_RETENTION_DAYS": "90",
}

func TestParseRunsOnlyUnderASwapValue(t *testing.T) {
	for _, sink := range []string{"", "postgres", "both"} {
		values := map[string]string{"DATABASE_URL": "postgres://example.invalid/accounts", "AUDIT_SINK": sink, "AUDIT_EXTERNAL_URL": "https://audit.example/ingest"}
		var stderr bytes.Buffer
		_, swap, code := parse(nil, env(values), &stderr)
		require.Nil(t, swap, sink)
		require.Equal(t, 1, code, sink)
		require.Contains(t, stderr.String(), "runs only under a swap value", sink)
	}

	var stderr bytes.Buffer
	opts, swap, code := parse([]string{"-through", "2026-06", "-confirm-drop", "-verify-only", "-expected-partitions-sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "-batch-size", "200"}, env(swapEnv), &stderr)
	require.Equal(t, 0, code, stderr.String())
	require.NotNil(t, swap)
	require.Equal(t, "deployment-1", swap.DeploymentID)
	require.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), *opts.through, "-through names the last month copied")
	require.True(t, opts.copyOptions.ConfirmDrop)
	require.True(t, opts.copyOptions.VerifyOnly)
	require.Equal(t, 200, opts.batchSize)
}

func TestParseRefusesWhatARunCannotStartFrom(t *testing.T) {
	for name, tc := range map[string]struct {
		args   []string
		values map[string]string
		code   int
		want   string
	}{
		"a malformed month":    {args: []string{"-through", "June"}, values: swapEnv, code: 2, want: "YYYY-MM"},
		"a stray argument":     {args: []string{"now"}, values: swapEnv, code: 2, want: "unexpected arguments"},
		"unbounded drop":       {args: []string{"-confirm-drop"}, values: swapEnv, code: 2, want: "drop requires"},
		"a swap missing parts": {values: map[string]string{"DATABASE_URL": "postgres://example.invalid/a", "AUDIT_SINK": "bigquery"}, code: 1, want: "invalid audit destination configuration"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, swap, code := parse(tc.args, env(tc.values), &stderr)
			require.Nil(t, swap)
			require.Equal(t, tc.code, code)
			require.Contains(t, stderr.String(), tc.want)
		})
	}
}

func TestSummarizeExitStatus(t *testing.T) {
	partition := business.AuditHistoryPartitionReport{
		Partition: business.AuditHistoryPartition{Name: "audit_events_2026_08"},
		Orgs:      map[string]business.AuditHistoryOrgCount{"": {Postgres: 2, Verified: 1}, "org-1": {Postgres: 3, Verified: 3}},
		Problems:  []string{"event e-1: missing from the store"},
		Failures:  4,
	}
	var stdout, stderr bytes.Buffer
	code := summarize(&stdout, &stderr, business.AuditHistoryReport{Partitions: []business.AuditHistoryPartitionReport{partition}}, business.ErrAuditHistoryUnverified)
	require.Equal(t, 3, code)
	require.Contains(t, stdout.String(), "audit_events_2026_08 (platform): 2 in Postgres, 1 verified")
	require.Contains(t, stdout.String(), "audit_events_2026_08 org-1: 3 in Postgres, 3 verified")
	require.Contains(t, stderr.String(), "missing from the store")
	require.Contains(t, stderr.String(), "and 3 more")

	stdout.Reset()
	require.Equal(t, 1, summarize(&stdout, &stderr, business.AuditHistoryReport{}, errors.New("warehouse unavailable")))
	require.Equal(t, 0, summarize(&stdout, &stderr, business.AuditHistoryReport{Verified: true}, nil))
	require.Contains(t, stdout.String(), "verified")
	stdout.Reset()
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, 0, summarize(&stdout, &stderr, business.AuditHistoryReport{Verified: true, Dropped: 2, Cutoff: cutoff}, nil))
	require.Contains(t, stdout.String(), "dropped 2 partitions ending at or before 2026-09-01T00:00:00Z")
}

func TestCapabilitiesAndHistoryMachineOutputExcludeSecretsAndProviderErrors(t *testing.T) {
	var out, stderr bytes.Buffer
	require.Equal(t, 0, RunHistory([]string{"-capabilities-json"}, env(swapEnv), &out, &stderr))
	require.Contains(t, out.String(), "codefly/audit-tools/v1")
	require.Contains(t, out.String(), "expected_partitions_sha256")
	require.NotContains(t, out.String(), "DATABASE_URL")
	require.NotContains(t, out.String(), "postgres://")
	out.Reset()
	_, swap, code := parse(nil, env(swapEnv), &stderr)
	require.Zero(t, code)
	require.Equal(t, 1, writeHistory(&out, options{json: true}, swap, business.AuditHistoryReport{}, errors.New("provider URL containing password=private")))
	require.NotContains(t, out.String(), "private")
	require.Contains(t, out.String(), "operation_failed")
	// Production takes its scoped Codefly capability, so absence of the explicit
	// integration-test URL is valid at parse time.
	values := map[string]string{}
	for k, v := range swapEnv {
		values[k] = v
	}
	delete(values, "DATABASE_URL")
	opts, swap, code := parse(nil, env(values), &stderr)
	require.Zero(t, code)
	require.NotNil(t, swap)
	require.Empty(t, opts.databaseURL)
}
