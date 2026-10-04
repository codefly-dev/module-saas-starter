// Package gcsarchive writes the audit store swap's locked archive (ADR 0009) to
// a Google Cloud Storage bucket: one JSON Lines object per relay batch, named
// and encoded as every archive is (archiveformat).
//
// The writer's authority is storage.objects.create alone. It never overwrites —
// every write carries a does-not-exist precondition — and never deletes; the
// bucket's retention lock (GCS Bucket Lock), which the deployment configures,
// is what keeps anyone else from doing either before the window ends.
// Credentials are Application Default Credentials only: the caller builds the
// client, and on Kubernetes that is the pod's workload identity.
package gcsarchive

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"strings"

	"accounts/pkg/auditstore/archiveformat"
	"accounts/pkg/business"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

// objectCreator creates one object only if none of that name exists.
type objectCreator interface {
	CreateObject(ctx context.Context, name string, body []byte) error
}

// Archive writes relay batches as archive objects.
type Archive struct {
	bucket objectCreator
}

// New writes into bucket through client, which the caller constructs with
// Application Default Credentials.
func New(client *storage.Client, bucket string) (*Archive, error) {
	if client == nil {
		return nil, errors.New("gcs audit archive: client is required")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("gcs audit archive: bucket is required")
	}
	return &Archive{bucket: gcsBucket{handle: client.Bucket(bucket)}}, nil
}

// WriteAuditBatch implements business.AuditArchive.
func (a *Archive) WriteAuditBatch(ctx context.Context, batch business.AuditBatch) error {
	if len(batch.Records) == 0 {
		return errors.New("gcs audit archive: refusing an empty batch")
	}
	name, err := archiveformat.ObjectName(batch)
	if err != nil {
		return err
	}
	body, err := archiveformat.Encode(batch)
	if err != nil {
		return err
	}
	if err := a.bucket.CreateObject(ctx, name, body); err != nil {
		return fmt.Errorf("gcs audit archive: write %s: %w", name, err)
	}
	return nil
}

// gcsBucket creates objects in one bucket.
type gcsBucket struct {
	handle *storage.BucketHandle
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// CreateObject uploads body in one request under a does-not-exist
// precondition, with its CRC32C so a corrupted upload is refused. The client
// retries the request; if a retry finds the object already there, that object
// is this upload's own — the name carries a batch id no other attempt uses —
// so the precondition failure is the acknowledgement it lost.
func (b gcsBucket) CreateObject(ctx context.Context, name string, body []byte) error {
	writer := b.handle.Object(name).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	writer.ContentType = archiveformat.ContentType
	writer.ChunkSize = len(body)
	writer.CRC32C = crc32.Checksum(body, castagnoli)
	writer.SendCRC32C = true
	if _, err := writer.Write(body); err != nil {
		_ = writer.Close()
		return err
	}
	err := writer.Close()
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusPreconditionFailed {
		return nil
	}
	return err
}
