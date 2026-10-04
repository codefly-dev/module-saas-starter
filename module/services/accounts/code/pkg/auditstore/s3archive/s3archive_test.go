package s3archive

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/auditstore/archiveformat"
	"accounts/pkg/business"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

var composedAt = time.Date(2026, 10, 2, 14, 30, 5, 987654321, time.UTC)

func batchOf(t *testing.T, id string, classes ...business.AuditRetentionClass) business.AuditBatch {
	t.Helper()
	batch := business.AuditBatch{ID: id, DeploymentID: "deployment-1", ComposedAt: composedAt}
	for _, class := range classes {
		record, err := business.NewAuditRecord(business.AuditEntry{
			ID:            business.NewIDString(),
			OrgID:         "22222222-2222-2222-2222-222222222222",
			ActorID:       "11111111-1111-1111-1111-111111111111",
			ActorType:     business.ActorTypeUser,
			EventType:     business.EventAuthLogin,
			SchemaVersion: 1,
			Resource:      "session",
			Payload:       map[string]any{"method": "password"},
			CreatedAt:     composedAt.Add(-time.Second),
		}, class)
		require.NoError(t, err)
		batch.Records = append(batch.Records, record)
	}
	return batch
}

// fakeS3 is a bucket behind an S3 endpoint that honours If-None-Match: * and
// verifies the SHA-256 a put carries. failNext answers the next put of a name
// with that status after storing it — an acknowledgement lost in transit.
type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	headers  []http.Header
	failNext map[string]int
	refuse   int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	f.headers = append(f.headers, r.Header.Clone())
	if f.refuse != 0 {
		w.WriteHeader(f.refuse)
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/audit-archive/")
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	if r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `<Error><Code>BadDigest</Code><Message>checksum</Message></Error>`)
		return
	}
	if _, exists := f.objects[key]; exists && r.Header.Get("If-None-Match") == "*" {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code><Message>exists</Message></Error>`)
		return
	}
	f.objects[key] = body
	if status := f.failNext[key]; status != 0 {
		delete(f.failNext, key)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `<Error><Code>InternalError</Code><Message>lost</Message></Error>`)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func fakeArchive(t *testing.T) (*Archive, *fakeS3) {
	t.Helper()
	fake := &fakeS3{objects: map[string][]byte{}, failNext: map[string]int{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(server.URL),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, PathStyleForEndpointOverride)
	archive, err := New(client, "audit-archive")
	require.NoError(t, err)
	return archive, fake
}

func TestWriteAuditBatchPutsOneObjectThatNeverOverwrites(t *testing.T) {
	archive, fake := fakeArchive(t)
	ctx := context.Background()
	batch := batchOf(t, "batch-1", business.RetentionSecurity, business.RetentionContent)
	require.NoError(t, archive.WriteAuditBatch(ctx, batch))

	name, err := archiveformat.ObjectName(batch)
	require.NoError(t, err)
	want, err := archiveformat.Encode(batch)
	require.NoError(t, err)
	require.Equal(t, map[string][]byte{name: want}, fake.objects, "one object, named and encoded as every archive is")

	sent := fake.headers[0]
	require.Equal(t, "*", sent.Get("If-None-Match"), "every put is create-only")
	require.Equal(t, archiveformat.ContentType, sent.Get("Content-Type"))
	require.NotEmpty(t, sent.Get("X-Amz-Checksum-Sha256"), "the put carries the object's SHA-256")
	require.Equal(t, "SHA256", sent.Get("X-Amz-Sdk-Checksum-Algorithm"))

	require.NoError(t, archive.WriteAuditBatch(ctx, batch),
		"a put that finds its own object (412) is the acknowledgement it lost")
	require.Equal(t, want, fake.objects[name], "and the object is not rewritten")
}

func TestWriteAuditBatchTreatsARetriedPutsPreconditionFailureAsItsAcknowledgement(t *testing.T) {
	archive, fake := fakeArchive(t)
	batch := batchOf(t, "batch-2", business.RetentionContent)
	name, err := archiveformat.ObjectName(batch)
	require.NoError(t, err)
	fake.failNext[name] = http.StatusInternalServerError

	require.NoError(t, archive.WriteAuditBatch(context.Background(), batch))
	require.Len(t, fake.headers, 2, "the SDK retried the put whose answer was lost")
	require.Len(t, fake.objects, 1)
}

func TestWriteAuditBatchRefusals(t *testing.T) {
	archive, fake := fakeArchive(t)
	ctx := context.Background()
	require.ErrorContains(t, archive.WriteAuditBatch(ctx, batchOf(t, "batch-3")), "empty batch")

	bad := batchOf(t, "../escape", business.RetentionContent)
	require.ErrorContains(t, archive.WriteAuditBatch(ctx, bad), "plain path segments")

	fake.refuse = http.StatusForbidden
	err := archive.WriteAuditBatch(ctx, batchOf(t, "batch-4", business.RetentionContent))
	require.ErrorContains(t, err, "AccessDenied", "any other refusal fails the delivery, which the relay retries")
	require.Empty(t, fake.objects)

	_, err = New(nil, "bucket")
	require.Error(t, err)
	_, err = newArchive(&s3.Client{}, " ")
	require.Error(t, err)
}

func TestNewFromEnvironmentNeedsARegion(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/absent")
	t.Setenv("AWS_PROFILE", "")
	_, err := NewFromEnvironment(context.Background(), "audit-archive")
	require.ErrorContains(t, err, "AWS_REGION")
}
