package main

import (
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestConfiguredAuditSinkDefaultsToPostgres(t *testing.T) {
	t.Setenv("AUDIT_SINK", "")
	sink, err := configuredAuditSink()
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkPostgres, sink.mode)
	require.Nil(t, sink.external)
	require.Nil(t, sink.bigQuery)

	t.Setenv("AUDIT_SINK", "postgres")
	sink, err = configuredAuditSink()
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkPostgres, sink.mode)
	require.False(t, sink.mode.Swaps())
}

func TestConfiguredAuditSinkBothRequiresExternalURL(t *testing.T) {
	t.Setenv("AUDIT_SINK", "both")
	t.Setenv("AUDIT_EXTERNAL_URL", "")
	_, err := configuredAuditSink()
	require.ErrorContains(t, err, "AUDIT_EXTERNAL_URL")

	t.Setenv("AUDIT_EXTERNAL_URL", "https://warehouse.example/audit")
	sink, err := configuredAuditSink()
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkBoth, sink.mode)
	require.False(t, sink.mode.Swaps())
	require.IsType(t, &business.HTTPAuditSink{}, sink.external)
}

func TestConfiguredAuditSinkRejectsExternalOnly(t *testing.T) {
	t.Setenv("AUDIT_SINK", "external")
	_, err := configuredAuditSink()
	require.ErrorContains(t, err, "not permitted")
	require.ErrorContains(t, err, "bigquery", "the refusal names the swap value that does what external cannot")
}

func TestConfiguredAuditSinkRejectsUnknownMode(t *testing.T) {
	for _, value := range []string{"kafka", "clickhouse"} {
		t.Setenv("AUDIT_SINK", value)
		_, err := configuredAuditSink()
		require.ErrorContains(t, err, "must be postgres, both or bigquery", value)
	}
}

// setBigQuerySwap sets every required bigquery setting to a valid value.
func setBigQuerySwap(t *testing.T) {
	t.Helper()
	t.Setenv("AUDIT_SINK", "bigquery")
	t.Setenv("AUDIT_BIGQUERY_PROJECT", "example-project")
	t.Setenv("AUDIT_BIGQUERY_DATASET", "audit")
	t.Setenv("AUDIT_ARCHIVE_URL", "gs://example-audit-archive")
	t.Setenv("AUDIT_DEPLOYMENT_ID", "acme-prod-1")
	t.Setenv("AUDIT_CONTENT_RETENTION_DAYS", "90")
	t.Setenv("AUDIT_RELAY_BATCH_SIZE", "")
	t.Setenv("AUDIT_RELAY_MAX_WAIT", "")
}

func TestConfiguredAuditSinkBigQuery(t *testing.T) {
	setBigQuerySwap(t)
	sink, err := configuredAuditSink()
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkBigQuery, sink.mode)
	require.True(t, sink.mode.Swaps())
	require.Nil(t, sink.external)
	require.Equal(t, &bigQueryAuditSwap{
		project:                "example-project",
		dataset:                "audit",
		archive:                auditArchiveLocation{scheme: "gs", bucket: "example-audit-archive"},
		deploymentID:           "acme-prod-1",
		contentDetailRetention: 90 * 24 * time.Hour,
		batchSize:              business.DefaultAuditRelayBatchSize,
		maxWait:                business.DefaultAuditRelayMaxWait,
	}, sink.bigQuery)

	t.Setenv("AUDIT_ARCHIVE_URL", "gs://example-audit-archive/")
	sink, err = configuredAuditSink()
	require.NoError(t, err, "a trailing slash still names only the bucket")
	require.Equal(t, "example-audit-archive", sink.bigQuery.archive.bucket)

	t.Setenv("AUDIT_RELAY_BATCH_SIZE", "200")
	t.Setenv("AUDIT_RELAY_MAX_WAIT", "750ms")
	sink, err = configuredAuditSink()
	require.NoError(t, err)
	require.Equal(t, 200, sink.bigQuery.batchSize)
	require.Equal(t, 750*time.Millisecond, sink.bigQuery.maxWait)
}

func TestConfiguredAuditSinkBigQueryNamesEveryMissingSetting(t *testing.T) {
	t.Setenv("AUDIT_SINK", "bigquery")
	for _, name := range []string{
		"AUDIT_BIGQUERY_PROJECT", "AUDIT_BIGQUERY_DATASET", "AUDIT_ARCHIVE_URL",
		"AUDIT_DEPLOYMENT_ID", "AUDIT_CONTENT_RETENTION_DAYS",
	} {
		t.Setenv(name, "")
	}
	_, err := configuredAuditSink()
	require.EqualError(t, err, "AUDIT_SINK=bigquery requires AUDIT_BIGQUERY_PROJECT, AUDIT_BIGQUERY_DATASET, "+
		"AUDIT_ARCHIVE_URL, AUDIT_DEPLOYMENT_ID, AUDIT_CONTENT_RETENTION_DAYS")

	setBigQuerySwap(t)
	t.Setenv("AUDIT_ARCHIVE_URL", "  ")
	_, err = configuredAuditSink()
	require.EqualError(t, err, "AUDIT_SINK=bigquery requires AUDIT_ARCHIVE_URL", "a blank value is a missing one")
}

func TestConfiguredAuditSinkBigQueryRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{"AUDIT_CONTENT_RETENTION_DAYS", "0", "AUDIT_CONTENT_RETENTION_DAYS"},
		{"AUDIT_CONTENT_RETENTION_DAYS", "90d", "AUDIT_CONTENT_RETENTION_DAYS"},
		{"AUDIT_CONTENT_RETENTION_DAYS", "-1", "AUDIT_CONTENT_RETENTION_DAYS"},
		{"AUDIT_DEPLOYMENT_ID", "acme/prod", "AUDIT_DEPLOYMENT_ID"},
		{"AUDIT_DEPLOYMENT_ID", "..", "AUDIT_DEPLOYMENT_ID"},
		{"AUDIT_ARCHIVE_URL", "example-audit-archive", "like gs://<bucket>"},
		{"AUDIT_ARCHIVE_URL", "s3://example-audit-archive", `scheme "s3" has no archive writer`},
		{"AUDIT_ARCHIVE_URL", "https://storage.example/archive", `scheme "https" has no archive writer`},
		{"AUDIT_ARCHIVE_URL", "gs://example-audit-archive/audit", "names a bucket and nothing else"},
		{"AUDIT_ARCHIVE_URL", "gs://user@example-audit-archive", "names a bucket and nothing else"},
		{"AUDIT_ARCHIVE_URL", "gs://Example_Archive", "not a valid bucket name"},
		{"AUDIT_RELAY_BATCH_SIZE", "0", "AUDIT_RELAY_BATCH_SIZE"},
		{"AUDIT_RELAY_BATCH_SIZE", "5001", "AUDIT_RELAY_BATCH_SIZE"},
		{"AUDIT_RELAY_MAX_WAIT", "5", "AUDIT_RELAY_MAX_WAIT"},
		{"AUDIT_RELAY_MAX_WAIT", "-1s", "AUDIT_RELAY_MAX_WAIT"},
		{"AUDIT_RELAY_MAX_WAIT", "2h", "AUDIT_RELAY_MAX_WAIT"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			setBigQuerySwap(t)
			t.Setenv(tc.name, tc.value)
			_, err := configuredAuditSink()
			require.ErrorContains(t, err, tc.want)
		})
	}
}
