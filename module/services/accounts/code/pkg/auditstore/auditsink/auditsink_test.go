package auditsink

import (
	"fmt"
	"os"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestConfiguredAuditSinkDefaultsToPostgres(t *testing.T) {
	t.Setenv("AUDIT_SINK", "")
	sink, err := Load(os.Getenv)
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkPostgres, sink.Mode)
	require.Nil(t, sink.External)
	require.Nil(t, sink.Swap)

	t.Setenv("AUDIT_SINK", "postgres")
	sink, err = Load(os.Getenv)
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkPostgres, sink.Mode)
	require.False(t, sink.Mode.Swaps())
}

func TestConfiguredAuditSinkBothRequiresExternalURL(t *testing.T) {
	t.Setenv("AUDIT_SINK", "both")
	t.Setenv("AUDIT_EXTERNAL_URL", "")
	_, err := Load(os.Getenv)
	require.ErrorContains(t, err, "AUDIT_EXTERNAL_URL")

	t.Setenv("AUDIT_EXTERNAL_URL", "https://warehouse.example/audit")
	sink, err := Load(os.Getenv)
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkBoth, sink.Mode)
	require.False(t, sink.Mode.Swaps())
	require.IsType(t, &business.HTTPAuditSink{}, sink.External)
}

func TestConfiguredAuditSinkRejectsExternalOnly(t *testing.T) {
	t.Setenv("AUDIT_SINK", "external")
	_, err := Load(os.Getenv)
	require.ErrorContains(t, err, "not permitted")
	require.ErrorContains(t, err, "bigquery or clickhouse", "the refusal names the swap values that do what external cannot")
}

func TestConfiguredAuditSinkRejectsUnknownMode(t *testing.T) {
	for _, value := range []string{"kafka", "redshift"} {
		t.Setenv("AUDIT_SINK", value)
		_, err := Load(os.Getenv)
		require.ErrorContains(t, err, "must be postgres, both, bigquery or clickhouse", value)
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
	sink, err := Load(os.Getenv)
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkBigQuery, sink.Mode)
	require.True(t, sink.Mode.Swaps())
	require.Nil(t, sink.External)
	require.Equal(t, &Swap{
		Mode:             business.AuditSinkBigQuery,
		BigQuery:         &BigQuery{Project: "example-project", Dataset: "audit"},
		Archive:          ArchiveLocation{Scheme: "gs", Bucket: "example-audit-archive"},
		DeploymentID:     "acme-prod-1",
		ContentRetention: 90 * 24 * time.Hour,
		RelayBatchSize:   business.DefaultAuditRelayBatchSize,
		RelayMaxWait:     business.DefaultAuditRelayMaxWait,
	}, sink.Swap)

	t.Setenv("AUDIT_ARCHIVE_URL", "gs://example-audit-archive/")
	sink, err = Load(os.Getenv)
	require.NoError(t, err, "a trailing slash still names only the bucket")
	require.Equal(t, "example-audit-archive", sink.Swap.Archive.Bucket)

	t.Setenv("AUDIT_RELAY_BATCH_SIZE", "200")
	t.Setenv("AUDIT_RELAY_MAX_WAIT", "750ms")
	sink, err = Load(os.Getenv)
	require.NoError(t, err)
	require.Equal(t, 200, sink.Swap.RelayBatchSize)
	require.Equal(t, 750*time.Millisecond, sink.Swap.RelayMaxWait)
}

func TestConfiguredAuditSinkBigQueryNamesEveryMissingSetting(t *testing.T) {
	t.Setenv("AUDIT_SINK", "bigquery")
	for _, name := range []string{
		"AUDIT_BIGQUERY_PROJECT", "AUDIT_BIGQUERY_DATASET", "AUDIT_ARCHIVE_URL",
		"AUDIT_DEPLOYMENT_ID", "AUDIT_CONTENT_RETENTION_DAYS",
	} {
		t.Setenv(name, "")
	}
	_, err := Load(os.Getenv)
	require.EqualError(t, err, "AUDIT_SINK=bigquery requires AUDIT_BIGQUERY_PROJECT, AUDIT_BIGQUERY_DATASET, "+
		"AUDIT_ARCHIVE_URL, AUDIT_DEPLOYMENT_ID, AUDIT_CONTENT_RETENTION_DAYS")

	setBigQuerySwap(t)
	t.Setenv("AUDIT_ARCHIVE_URL", "  ")
	_, err = Load(os.Getenv)
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
		{"AUDIT_ARCHIVE_URL", "example-audit-archive", "like gs://<bucket> or s3://<bucket>"},
		{"AUDIT_ARCHIVE_URL", "azblob://example-audit-archive", `scheme "azblob" has no archive writer; the supported schemes are gs:// and s3://`},
		{"AUDIT_ARCHIVE_URL", "https://storage.example/archive", `scheme "https" has no archive writer`},
		{"AUDIT_ARCHIVE_URL", "s3://example-audit-archive/audit", "names a bucket and nothing else, like s3://<bucket>"},
		{"AUDIT_ARCHIVE_URL", "s3://example_audit_archive", "not a valid bucket name"},
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
			_, err := Load(os.Getenv)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestConfiguredAuditSinkTakesAnS3Archive(t *testing.T) {
	setBigQuerySwap(t)
	t.Setenv("AUDIT_ARCHIVE_URL", "s3://example-audit-archive")
	sink, err := Load(os.Getenv)
	require.NoError(t, err, "any warehouse pairs with any archive")
	require.Equal(t, ArchiveLocation{Scheme: "s3", Bucket: "example-audit-archive"}, sink.Swap.Archive)
}

// setClickHouseSwap sets every required clickhouse setting to a valid value.
func setClickHouseSwap(t *testing.T) {
	t.Helper()
	t.Setenv("AUDIT_SINK", "clickhouse")
	t.Setenv("AUDIT_CLICKHOUSE_DSN", "clickhouse://audit:s3cr3t-value@clickhouse.example:9440/audit?secure=true")
	t.Setenv("AUDIT_CLICKHOUSE_CLUSTER", "")
	t.Setenv("AUDIT_EVENTS_RETENTION_DAYS", "2555")
	t.Setenv("AUDIT_ARCHIVE_URL", "s3://example-audit-archive")
	t.Setenv("AUDIT_DEPLOYMENT_ID", "acme-prod-1")
	t.Setenv("AUDIT_CONTENT_RETENTION_DAYS", "90")
	t.Setenv("AUDIT_RELAY_BATCH_SIZE", "")
	t.Setenv("AUDIT_RELAY_MAX_WAIT", "")
}

func TestConfiguredAuditSinkClickHouse(t *testing.T) {
	setClickHouseSwap(t)
	sink, err := Load(os.Getenv)
	require.NoError(t, err)
	require.Equal(t, business.AuditSinkClickHouse, sink.Mode)
	require.True(t, sink.Mode.Swaps())
	require.Equal(t, &Swap{
		Mode: business.AuditSinkClickHouse,
		ClickHouse: &ClickHouse{
			DSN:             "clickhouse://audit:s3cr3t-value@clickhouse.example:9440/audit?secure=true",
			Database:        "audit",
			EventsRetention: 2555 * 24 * time.Hour,
		},
		Archive:          ArchiveLocation{Scheme: "s3", Bucket: "example-audit-archive"},
		DeploymentID:     "acme-prod-1",
		ContentRetention: 90 * 24 * time.Hour,
		RelayBatchSize:   business.DefaultAuditRelayBatchSize,
		RelayMaxWait:     business.DefaultAuditRelayMaxWait,
	}, sink.Swap)
	require.Nil(t, sink.Swap.BigQuery)

	t.Setenv("AUDIT_CLICKHOUSE_CLUSTER", "{cluster}")
	t.Setenv("AUDIT_ARCHIVE_URL", "gs://example-audit-archive")
	sink, err = Load(os.Getenv)
	require.NoError(t, err)
	require.Equal(t, "{cluster}", sink.Swap.ClickHouse.Cluster)
	require.Equal(t, "gs", sink.Swap.Archive.Scheme, "any warehouse pairs with any archive")

	t.Setenv("AUDIT_EVENTS_RETENTION_DAYS", "90")
	_, err = Load(os.Getenv)
	require.NoError(t, err, "the events window may equal the content window")
}

func TestConfiguredAuditSinkClickHouseNamesEveryMissingSetting(t *testing.T) {
	t.Setenv("AUDIT_SINK", "clickhouse")
	for _, name := range []string{
		"AUDIT_CLICKHOUSE_DSN", "AUDIT_ARCHIVE_URL", "AUDIT_DEPLOYMENT_ID",
		"AUDIT_CONTENT_RETENTION_DAYS", "AUDIT_EVENTS_RETENTION_DAYS",
		"AUDIT_BIGQUERY_PROJECT", "AUDIT_BIGQUERY_DATASET",
	} {
		t.Setenv(name, "")
	}
	_, err := Load(os.Getenv)
	require.EqualError(t, err, "AUDIT_SINK=clickhouse requires AUDIT_CLICKHOUSE_DSN, AUDIT_ARCHIVE_URL, "+
		"AUDIT_DEPLOYMENT_ID, AUDIT_CONTENT_RETENTION_DAYS, AUDIT_EVENTS_RETENTION_DAYS")

	setClickHouseSwap(t)
	t.Setenv("AUDIT_CLICKHOUSE_DSN", " \n")
	_, err = Load(os.Getenv)
	require.EqualError(t, err, "AUDIT_SINK=clickhouse requires AUDIT_CLICKHOUSE_DSN", "a blank value is a missing one")
}

func TestConfiguredAuditSinkClickHouseRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{"AUDIT_EVENTS_RETENTION_DAYS", "0", "AUDIT_EVENTS_RETENTION_DAYS must be a whole number of days"},
		{"AUDIT_EVENTS_RETENTION_DAYS", "7y", "AUDIT_EVENTS_RETENTION_DAYS must be a whole number of days"},
		{"AUDIT_EVENTS_RETENTION_DAYS", "30", "AUDIT_EVENTS_RETENTION_DAYS (30) must be at least AUDIT_CONTENT_RETENTION_DAYS (90)"},
		{"AUDIT_CONTENT_RETENTION_DAYS", "0", "AUDIT_CONTENT_RETENTION_DAYS"},
		{"AUDIT_CLICKHOUSE_DSN", "clickhouse://audit:s3cr3t-value@clickhouse.example:9440", "must name the database"},
		{"AUDIT_CLICKHOUSE_DSN", "clickhouse://audit:s3cr3t-value@/audit", "not a valid ClickHouse DSN"},
		{"AUDIT_CLICKHOUSE_DSN", "clickhouse://audit:s3cr3t-value@clickhouse.example:9440/audit?dial_timeout=s3cr3t-value", "not a valid ClickHouse DSN"},
		{"AUDIT_CLICKHOUSE_DSN", "clickhouse://audit:s3cr3t-value\x7f@clickhouse.example/audit", "not a valid ClickHouse DSN"},
		{"AUDIT_CLICKHOUSE_CLUSTER", "prod' ON CLUSTER 'x", "AUDIT_CLICKHOUSE_CLUSTER must be a cluster name"},
		{"AUDIT_DEPLOYMENT_ID", "acme/prod", "AUDIT_DEPLOYMENT_ID"},
		{"AUDIT_ARCHIVE_URL", "azblob://example-audit-archive", "has no archive writer"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			setClickHouseSwap(t)
			t.Setenv(tc.name, tc.value)
			_, err := Load(os.Getenv)
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "s3cr3t-value", "a refusal never echoes the DSN's credentials")
		})
	}
}

func TestClickHouseSettingsNeverPrintTheDSN(t *testing.T) {
	setClickHouseSwap(t)
	sink, err := Load(os.Getenv)
	require.NoError(t, err)
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		require.NotContains(t, fmt.Sprintf(format, *sink.Swap.ClickHouse), "s3cr3t-value", format)
		require.NotContains(t, fmt.Sprintf(format, sink.Swap.ClickHouse), "s3cr3t-value", format)
	}
}
