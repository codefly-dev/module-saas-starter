package gcsarchive

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/auditstore/archiveformat"
	"accounts/pkg/business"

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
			Payload:       map[string]any{"method": "password", "note": "<b>&"},
			CreatedAt:     composedAt.Add(-time.Second),
		}, class)
		require.NoError(t, err)
		batch.Records = append(batch.Records, record)
	}
	return batch
}

// fakeBucket records created objects and refuses to overwrite one.
type fakeBucket struct {
	objects map[string][]byte
	fail    error
}

func (b *fakeBucket) CreateObject(_ context.Context, name string, body []byte) error {
	if b.fail != nil {
		return b.fail
	}
	if _, exists := b.objects[name]; exists {
		return errors.New("precondition failed: object exists")
	}
	b.objects[name] = body
	return nil
}

func TestWriteAuditBatchWritesOneObjectPerBatch(t *testing.T) {
	bucket := &fakeBucket{objects: map[string][]byte{}}
	archive := &Archive{bucket: bucket}

	first := batchOf(t, "batch-1", business.RetentionSecurity, business.RetentionContent, business.RetentionContent)
	require.NoError(t, archive.WriteAuditBatch(context.Background(), first))
	require.Len(t, bucket.objects, 1, "a whole batch is one object")
	name, err := archiveformat.ObjectName(first)
	require.NoError(t, err)
	want, err := archiveformat.Encode(first)
	require.NoError(t, err)
	require.Equal(t, want, bucket.objects[name])

	require.Error(t, archive.WriteAuditBatch(context.Background(), first), "an object is never overwritten")
	require.Error(t, archive.WriteAuditBatch(context.Background(), batchOf(t, "batch-2")), "an empty batch is refused")

	bucket.fail = errors.New("bucket unavailable")
	require.ErrorContains(t, archive.WriteAuditBatch(context.Background(), batchOf(t, "batch-3", business.RetentionContent)), "bucket unavailable")
}

func TestNewValidatesItsConfiguration(t *testing.T) {
	_, err := New(nil, "bucket")
	require.Error(t, err)
}
