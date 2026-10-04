package auditsink

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auditstore/s3archive"
	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Open end to end — the settings as the service reads them, a ClickHouse store
// of record and an S3 archive — when both servers are reachable (see
// clickhousestore and s3archive for how to run them):
//
//	AUDIT_CLICKHOUSE_TEST_DSN=clickhouse://<user>:<password>@127.0.0.1:9000/default \
//	AUDIT_S3_TEST_ENDPOINT=http://127.0.0.1:9002 AWS_ACCESS_KEY_ID=<user> AWS_SECRET_ACCESS_KEY=<password> \
//	    AWS_REGION=us-east-1 go test ./pkg/auditstore/auditsink
//
// It is skipped otherwise. The test creates the database and the bucket; Open
// creates neither.
func TestOpenClickHouseWithAnS3Archive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AUDIT_CLICKHOUSE_TEST_DSN"))
	endpoint := strings.TrimSpace(os.Getenv("AUDIT_S3_TEST_ENDPOINT"))
	if dsn == "" || endpoint == "" {
		t.Skip("AUDIT_CLICKHOUSE_TEST_DSN and AUDIT_S3_TEST_ENDPOINT are not both set")
	}
	ctx := context.Background()
	options, err := clickhouse.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := clickhouse.Open(options)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	database := "audit_open_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, admin.Exec(ctx, "CREATE DATABASE "+database))
	t.Cleanup(func() { _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database) })

	t.Setenv("AWS_ENDPOINT_URL_S3", endpoint)
	awsConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	bucket := "audit-open-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	_, err = s3.NewFromConfig(awsConfig, s3archive.PathStyleForEndpointOverride).CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	scoped, err := url.Parse(dsn)
	require.NoError(t, err)
	scoped.Path = "/" + database
	setClickHouseSwap(t)
	t.Setenv("AUDIT_CLICKHOUSE_DSN", scoped.String())
	t.Setenv("AUDIT_ARCHIVE_URL", "s3://"+bucket)
	sink, err := Load(os.Getenv)
	require.NoError(t, err)
	opened, err := Open(ctx, sink.Swap)
	require.NoError(t, err)
	t.Cleanup(opened.Close)

	org := uuid.NewString()
	record, err := business.NewAuditRecord(business.AuditEntry{
		ID: uuid.NewString(), OrgID: org, ActorType: business.ActorTypeSystem, EventType: business.EventAuthLogin,
		SchemaVersion: 1, Resource: "session", Payload: map[string]any{"method": "password"},
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}, business.RetentionSecurity)
	require.NoError(t, err)
	batch := business.AuditBatch{ID: uuid.NewString(), DeploymentID: sink.Swap.DeploymentID, ComposedAt: time.Now(), Records: []business.AuditRecord{record}}
	require.NoError(t, opened.Store.AppendAuditBatch(ctx, batch))
	require.NoError(t, opened.Archive.WriteAuditBatch(ctx, batch))

	entries, _, err := opened.Store.ListAuditEvents(ctx, business.AuditRead{
		Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org},
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, record.Entry.ID, entries[0].ID)
	require.Equal(t, map[string]any{"method": "password"}, entries[0].Payload)
}
