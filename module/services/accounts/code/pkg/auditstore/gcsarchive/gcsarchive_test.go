package gcsarchive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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

func TestObjectNameIsPerBatchUnderTheDeploymentAndDay(t *testing.T) {
	name, err := ObjectName(batchOf(t, "4b6d0d1e-0f3c-4c1a-9a55-6f0c2b1d7e10"))
	require.NoError(t, err)
	require.Equal(t, "deployment-1/2026/10/02/20261002T143005.987654321Z-4b6d0d1e-0f3c-4c1a-9a55-6f0c2b1d7e10.jsonl", name)

	other, err := ObjectName(batchOf(t, "9e2f4a6b-1c3d-4e5f-8a7b-0c1d2e3f4a5b"))
	require.NoError(t, err)
	require.NotEqual(t, name, other, "a redelivered batch carries a new id, so it is a new object")

	for _, bad := range []business.AuditBatch{
		{ID: "batch", DeploymentID: "../escape", ComposedAt: composedAt},
		{ID: "a/b", DeploymentID: "deployment-1", ComposedAt: composedAt},
		{ID: "", DeploymentID: "deployment-1", ComposedAt: composedAt},
	} {
		_, err := ObjectName(bad)
		require.Error(t, err, "%+v", bad)
	}
}

func TestEncodeWritesSecurityInFullAndContentWithoutItsDetails(t *testing.T) {
	batch := batchOf(t, "batch-1", business.RetentionSecurity, business.RetentionContent)
	body, err := Encode(batch)
	require.NoError(t, err)

	var lines []Line
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		var line Line
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line))
		lines = append(lines, line)
	}
	require.Len(t, lines, 2, "one line per record, in batch order")

	security, content := lines[0], lines[1]
	require.Equal(t, batch.Records[0].Entry.ID, security.EventID)
	require.Equal(t, "security", security.RetentionClass)
	require.Equal(t, `{"method":"password","note":"<b>&"}`, security.Details)
	require.Equal(t, business.AuditDetailsSHA256(security.Details), security.DetailsSHA256,
		"a reader verifies a line by hashing its details string")

	require.Equal(t, batch.Records[1].Entry.ID, content.EventID)
	require.Equal(t, "content", content.RetentionClass)
	require.Empty(t, content.Details, "a content-class line is content-free")
	require.Equal(t, batch.Records[1].DetailsSHA256, content.DetailsSHA256, "its hash proves what the details table holds")
	require.NotContains(t, string(body[bytes.IndexByte(body, '\n')+1:]), "password")

	for _, line := range lines {
		require.Equal(t, "deployment-1", line.DeploymentID)
		require.Equal(t, "batch-1", line.BatchID)
		require.Equal(t, "22222222-2222-2222-2222-222222222222", line.OrgID)
		require.Equal(t, "2026-10-02T14:30:04.987654321Z", line.OccurredAt)
	}
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
	name, err := ObjectName(first)
	require.NoError(t, err)
	want, err := Encode(first)
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
