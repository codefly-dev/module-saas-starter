package s3archive

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"accounts/pkg/auditstore/archiveformat"
	"accounts/pkg/business"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The writer against an S3-compatible store with Object Lock, when one is
// reachable:
//
//	docker run --rm -p 9000:9000 -e MINIO_ROOT_USER=<user> -e MINIO_ROOT_PASSWORD=<password> \
//	    quay.io/minio/minio server /data
//	AUDIT_S3_TEST_ENDPOINT=http://127.0.0.1:9000 AWS_ACCESS_KEY_ID=<user> AWS_SECRET_ACCESS_KEY=<password> \
//	    AWS_REGION=us-east-1 go test ./pkg/auditstore/s3archive
//
// It is skipped otherwise, and no gate runs it. The test creates a bucket with
// Object Lock and a default retention — the writer itself never creates or
// configures a bucket; the deployment does — and the writer is built the way
// the service builds it, from the environment.
func TestObjectLockBucket(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AUDIT_S3_TEST_ENDPOINT"))
	if endpoint == "" {
		t.Skip("AUDIT_S3_TEST_ENDPOINT is not set; see the comment above for how to run against an S3-compatible store")
	}
	t.Setenv("AWS_ENDPOINT_URL_S3", endpoint)
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	admin := s3.NewFromConfig(cfg, PathStyleForEndpointOverride)

	bucket := "audit-archive-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	_, err = admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket), ObjectLockEnabledForBucket: aws.Bool(true)})
	require.NoError(t, err)
	_, err = admin.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
		ObjectLockConfiguration: &types.ObjectLockConfiguration{
			ObjectLockEnabled: types.ObjectLockEnabledEnabled,
			Rule: &types.ObjectLockRule{DefaultRetention: &types.DefaultRetention{
				Mode: types.ObjectLockRetentionModeGovernance, Days: aws.Int32(1),
			}},
		},
	})
	require.NoError(t, err)

	archive, err := NewFromEnvironment(ctx, bucket)
	require.NoError(t, err)
	first := batchOf(t, uuid.NewString(), business.RetentionSecurity, business.RetentionContent)
	require.NoError(t, archive.WriteAuditBatch(ctx, first), "an Object Lock bucket accepts the put: it carries a checksum")
	require.NoError(t, archive.WriteAuditBatch(ctx, first), "a second put of the same name is refused by the store and taken as acknowledged")
	second := batchOf(t, uuid.NewString(), business.RetentionContent)
	require.NoError(t, archive.WriteAuditBatch(ctx, second))

	listed, err := admin.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, listed.Versions, 2, "one object per batch, and the repeated put wrote no new version")

	for _, batch := range []business.AuditBatch{first, second} {
		name, err := archiveformat.ObjectName(batch)
		require.NoError(t, err)
		object, err := admin.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(name)})
		require.NoError(t, err)
		body, err := io.ReadAll(object.Body)
		require.NoError(t, object.Body.Close())
		require.NoError(t, err)
		want, err := archiveformat.Encode(batch)
		require.NoError(t, err)
		require.Equal(t, string(want), string(body))
		require.Equal(t, archiveformat.ContentType, aws.ToString(object.ContentType))

		retention, err := admin.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String(name)})
		require.NoError(t, err)
		require.Equal(t, types.ObjectLockRetentionModeGovernance, retention.Retention.Mode, "the bucket's default retention locks every object")
	}
}
