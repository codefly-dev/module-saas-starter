package auditops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// historyNow is after July 2026 and inside October, so -through 2026-09 is the
// last completed month and 2026-10 the current one.
var historyNow = time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)

func TestParseRunsOnlyUnderASwapValue(t *testing.T) {
	for _, sink := range []string{"", "postgres", "both"} {
		values := map[string]string{"DATABASE_URL": "postgres://example.invalid/accounts", "AUDIT_SINK": sink, "AUDIT_EXTERNAL_URL": "https://audit.example/ingest"}
		var stderr bytes.Buffer
		_, swap, code := parse(nil, env(values), &stderr, historyNow)
		require.Nil(t, swap, sink)
		require.Equal(t, 1, code, sink)
		require.Contains(t, stderr.String(), "runs only under a swap value", sink)
	}

	var stderr bytes.Buffer
	opts, swap, code := parse([]string{"-through", "2026-06", "-confirm-drop", "-verify-only", "-expected-partitions-sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "-batch-size", "200"}, env(swapEnv), &stderr, historyNow)
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
		"the current month":    {args: []string{"-through", "2026-10"}, values: swapEnv, code: 2, want: "completed month"},
		"a future month":       {args: []string{"-through", "2027-01"}, values: swapEnv, code: 2, want: "completed month"},
		"a batch size of zero": {args: []string{"-batch-size", "0"}, values: swapEnv, code: 2, want: "-batch-size must be between 1 and"},
		"a negative batch":     {args: []string{"-batch-size", "-5"}, values: swapEnv, code: 2, want: "-batch-size must be between 1 and"},
		"a batch past the cap": {args: []string{"-batch-size", fmt.Sprint(business.MaxAuditRelayBatchSize + 1)}, values: swapEnv, code: 2, want: "-batch-size must be between 1 and"},
		"a stray argument":     {args: []string{"now"}, values: swapEnv, code: 2, want: "unexpected arguments"},
		"unbounded drop":       {args: []string{"-confirm-drop"}, values: swapEnv, code: 2, want: "drop requires"},
		"a swap missing parts": {values: map[string]string{"DATABASE_URL": "postgres://example.invalid/a", "AUDIT_SINK": "bigquery"}, code: 1, want: "invalid audit destination configuration"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, swap, code := parse(tc.args, env(tc.values), &stderr, historyNow)
			require.Nil(t, swap)
			require.Equal(t, tc.code, code)
			require.Contains(t, stderr.String(), tc.want)
		})
	}
}

func TestParseAcceptsTheLastCompletedMonth(t *testing.T) {
	var stderr bytes.Buffer
	opts, swap, code := parse([]string{"-through", "2026-09", "-batch-size", fmt.Sprint(business.MaxAuditRelayBatchSize)}, env(swapEnv), &stderr, historyNow)
	require.Equal(t, 0, code, stderr.String())
	require.NotNil(t, swap)
	require.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), *opts.through)
	// The month ends the instant it ends: at that instant it is complete.
	_, _, code = parse([]string{"-through", "2026-09"}, env(swapEnv), &stderr, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	require.Equal(t, 0, code)
	_, _, code = parse([]string{"-through", "2026-09"}, env(swapEnv), &stderr, time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC))
	require.Equal(t, 2, code)
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
	require.Equal(t, 0, summarize(&stdout, &stderr, business.AuditHistoryReport{
		Verified: true, Dropped: 2, Cutoff: cutoff, DroppedNames: []string{"audit_events_2026_07", "audit_events_2026_08"},
	}, nil))
	require.Contains(t, stdout.String(), "dropped 2 partitions ending at or before 2026-09-01T00:00:00Z: audit_events_2026_07, audit_events_2026_08")
}

// A refused drop, a run with nothing to verify and a run that found events
// outside the verified partitions each say what they found and exit with a
// status of their own, so a pipeline can tell them from a failure.
func TestSummarizeDistinguishesRefusedNothingToVerifyAndUncovered(t *testing.T) {
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	var stdout, stderr bytes.Buffer
	refused := business.AuditHistoryReport{Verified: true, Cutoff: cutoff, DropRefused: "partition audit_events_2026_08 holds 6 rows, 5 were verified"}
	require.Equal(t, 4, summarize(&stdout, &stderr, refused, fmt.Errorf("%w: %s", business.ErrAuditHistoryDropRefused, refused.DropRefused)))
	require.Contains(t, stderr.String(), "drop refused: partition audit_events_2026_08 holds 6 rows, 5 were verified")
	require.Contains(t, stderr.String(), "nothing was dropped")
	require.NotContains(t, stderr.String(), "operation_failed")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 5, summarize(&stdout, &stderr, business.AuditHistoryReport{Cutoff: cutoff}, business.ErrAuditHistoryNothingToVerify))
	require.Contains(t, stderr.String(), "nothing to verify")
	require.Contains(t, stderr.String(), "2026-09-01T00:00:00Z")
	require.NotContains(t, stdout.String(), "verified\n", "a run with nothing to verify does not print the success line")

	stdout.Reset()
	stderr.Reset()
	uncovered := business.AuditHistoryReport{Cutoff: cutoff, Uncovered: []business.AuditHistoryTableRows{{Table: "audit_events_default", Rows: 7}}}
	require.Equal(t, 3, summarize(&stdout, &stderr, uncovered, business.ErrAuditHistoryUnverified))
	require.Contains(t, stderr.String(), "audit_events_default holds 7 events older than 2026-09-01T00:00:00Z that no verified partition covers")
}

func TestHistoryReceiptCarriesTheSameOutcomes(t *testing.T) {
	var stderr bytes.Buffer
	_, swap, code := parse(nil, env(swapEnv), &stderr, historyNow)
	require.Zero(t, code)
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		report business.AuditHistoryReport
		err    error
		code   int
		want   string
	}{
		"verified":        {report: business.AuditHistoryReport{Verified: true, Cutoff: cutoff}, code: 0, want: ""},
		"unverified":      {report: business.AuditHistoryReport{Cutoff: cutoff}, err: business.ErrAuditHistoryUnverified, code: 3, want: "verification_failed"},
		"nothing":         {report: business.AuditHistoryReport{Cutoff: cutoff}, err: business.ErrAuditHistoryNothingToVerify, code: 5, want: "nothing_to_verify"},
		"refused":         {report: business.AuditHistoryReport{Verified: true, Cutoff: cutoff, DropRefused: "partition audit_events_2026_08 holds 6 rows, 5 were verified"}, err: fmt.Errorf("%w: x", business.ErrAuditHistoryDropRefused), code: 4, want: "drop_refused"},
		"another failure": {report: business.AuditHistoryReport{}, err: errors.New("warehouse unavailable"), code: 1, want: "operation_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			require.Equal(t, tc.code, writeHistory(&out, options{json: true}, swap, tc.report, tc.err))
			var receipt HistoryReceipt
			require.NoError(t, json.Unmarshal(out.Bytes(), &receipt))
			require.Equal(t, tc.want, receipt.ErrorCode)
			require.Equal(t, tc.report.Verified, receipt.Verified)
			require.Equal(t, tc.report.DropRefused, receipt.DropRefused)
			require.NotNil(t, receipt.Uncovered, "present as an empty list when there is none")
		})
	}

	var out bytes.Buffer
	uncovered := business.AuditHistoryReport{Cutoff: cutoff, Uncovered: []business.AuditHistoryTableRows{{Table: "audit_events_default", Rows: 7}}}
	require.Equal(t, 3, writeHistory(&out, options{json: true}, swap, uncovered, business.ErrAuditHistoryUnverified))
	var receipt HistoryReceipt
	require.NoError(t, json.Unmarshal(out.Bytes(), &receipt))
	require.Equal(t, []HistoryUncovered{{Table: "audit_events_default", Rows: 7}}, receipt.Uncovered)
}

// A receipt identifies the destination by what is not secret. A ClickHouse store
// is its database and cluster, which the DSN's host and password are not.
func TestReceiptsIdentifyAClickHouseDestinationWithoutItsDSN(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":                 "postgres://example.invalid/accounts",
		"AUDIT_SINK":                   "clickhouse",
		"AUDIT_CLICKHOUSE_DSN":         "clickhouse://audit_writer:hunter2-secret@clickhouse.internal.example:9000/audit_db",
		"AUDIT_CLICKHOUSE_CLUSTER":     "audit_cluster",
		"AUDIT_EVENTS_RETENTION_DAYS":  "400",
		"AUDIT_ARCHIVE_URL":            "gs://example-audit-archive",
		"AUDIT_DEPLOYMENT_ID":          "deployment-1",
		"AUDIT_CONTENT_RETENTION_DAYS": "90",
	}
	var stderr bytes.Buffer
	_, swap, code := parse(nil, env(values), &stderr, historyNow)
	require.Zero(t, code, stderr.String())

	var history bytes.Buffer
	require.Equal(t, 0, writeHistory(&history, options{json: true}, swap, business.AuditHistoryReport{Verified: true}, nil))
	var capabilities bytes.Buffer
	require.Equal(t, 0, RunHistory([]string{"-capabilities-json"}, env(values), &capabilities, &stderr))
	for _, body := range []string{history.String(), capabilities.String()} {
		var receipt struct {
			Configuration map[string]string `json:"configuration"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &receipt))
		require.Equal(t, "audit_db", receipt.Configuration["AUDIT_CLICKHOUSE_DATABASE"])
		require.Equal(t, "audit_cluster", receipt.Configuration["AUDIT_CLICKHOUSE_CLUSTER"])
		require.Equal(t, "400", receipt.Configuration["AUDIT_EVENTS_RETENTION_DAYS"])
		for _, secret := range []string{"hunter2-secret", "audit_writer", "clickhouse.internal.example", "clickhouse://", "AUDIT_CLICKHOUSE_DSN"} {
			require.NotContains(t, body, secret)
		}
	}

	// A ClickHouse store with no cluster says so; two stores that differ only in
	// the database are told apart.
	delete(values, "AUDIT_CLICKHOUSE_CLUSTER")
	values["AUDIT_CLICKHOUSE_DSN"] = "clickhouse://audit_writer:hunter2-secret@clickhouse.internal.example:9000/other_db"
	_, swap, code = parse(nil, env(values), &stderr, historyNow)
	require.Zero(t, code)
	configuration := safeConfiguration(swap)
	require.Equal(t, "other_db", configuration["AUDIT_CLICKHOUSE_DATABASE"])
	require.Contains(t, configuration, "AUDIT_CLICKHOUSE_CLUSTER")
	require.Empty(t, configuration["AUDIT_CLICKHOUSE_CLUSTER"])
}

func TestCapabilitiesAndHistoryMachineOutputExcludeSecretsAndProviderErrors(t *testing.T) {
	var out, stderr bytes.Buffer
	require.Equal(t, 0, RunHistory([]string{"-capabilities-json"}, env(swapEnv), &out, &stderr))
	require.Contains(t, out.String(), "codefly/audit-tools/v1")
	require.Contains(t, out.String(), "expected_partitions_sha256")
	require.NotContains(t, out.String(), "DATABASE_URL")
	require.NotContains(t, out.String(), "postgres://")
	out.Reset()
	_, swap, code := parse(nil, env(swapEnv), &stderr, historyNow)
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
	opts, swap, code := parse(nil, env(values), &stderr, historyNow)
	require.Zero(t, code)
	require.NotNil(t, swap)
	require.Empty(t, opts.databaseURL)
}
