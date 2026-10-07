// Package gcs implements the witness's conditional object-store seam.
package gcs

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"regexp"

	"policy-log/objectstore"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

var entryName = regexp.MustCompile(`^entries/[0-9]{20}\.json$`)

// GCS owns a bucket in the witness trust domain, never a host database or a
// host-controlled bucket. It performs one attempt per write, even for 5xx.
type GCS struct {
	client *storage.Client
	bucket *storage.BucketHandle
}

// New uses the service's workload identity and the official GCS endpoint. There
// is no endpoint or emulator fallback in production. Bucket identity is delivery
// data; IAM and the service's own workload identity enforce its trust domain.
func New(ctx context.Context, bucket string) (*GCS, error) {
	if bucket == "" {
		return nil, errors.New("policy-log: GCS bucket is required")
	}
	if os.Getenv("STORAGE_EMULATOR_HOST") != "" {
		return nil, errors.New("policy-log: storage emulator is forbidden")
	}
	client, err := storage.NewClient(ctx, storage.WithJSONReads())
	if err != nil {
		return nil, err
	}
	return newGCS(client, bucket), nil
}

func newGCS(client *storage.Client, bucket string) *GCS {
	return &GCS{client: client, bucket: client.Bucket(bucket).Retryer(storage.WithPolicy(storage.RetryNever))}
}

func (s *GCS) Close() error { return s.client.Close() }

func (s *GCS) Read(ctx context.Context, name string) (objectstore.Object, error) {
	if err := ctx.Err(); err != nil {
		return objectstore.Object{}, err
	}
	if name != objectstore.HeadObject && !entryName.MatchString(name) {
		return objectstore.Object{}, errors.New("invalid policy-log object name")
	}
	reader, err := s.bucket.Object(name).NewReader(ctx)
	if err != nil {
		return objectstore.Object{}, classify(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, objectstore.MaxObjectBytes+1))
	if err != nil {
		return objectstore.Object{}, classify(err)
	}
	if err := ctx.Err(); err != nil {
		return objectstore.Object{}, err
	}
	if len(data) > objectstore.MaxObjectBytes || reader.Attrs.Generation <= 0 {
		return objectstore.Object{}, errors.New("invalid policy-log object size or generation")
	}
	// Data and generation come from ONE response, never an Attrs/read race.
	return objectstore.Object{Data: data, Generation: reader.Attrs.Generation}, nil
}

func (s *GCS) Write(ctx context.Context, name string, data []byte, generation int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) > objectstore.MaxObjectBytes || generation < 0 {
		return 0, errors.New("invalid policy-log object write")
	}
	if name == objectstore.HeadObject {
		// Genesis is provisioning, not a runtime reset path.
		if generation == 0 {
			return 0, errors.New("policy-log head must be provisioned before boot")
		}
	} else if !entryName.MatchString(name) || generation != 0 {
		return 0, errors.New("entries must be immutable, sequence-named objects")
	}
	condition := storage.Conditions{GenerationMatch: generation}
	if generation == 0 {
		condition = storage.Conditions{DoesNotExist: true}
	}
	writer := s.bucket.Object(name).If(condition).NewWriter(ctx)
	writer.ChunkSize = 0 // one bounded upload; no resumable session or retry
	writer.ContentType = "application/json"
	writer.CacheControl = "no-store"
	writer.CRC32C = crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))
	writer.SendCRC32C = true
	if _, err := writer.Write(data); err != nil {
		_ = writer.CloseWithError(err)
		return 0, classify(err)
	}
	if err := ctx.Err(); err != nil {
		_ = writer.CloseWithError(err)
		return 0, err
	}
	if err := writer.Close(); err != nil {
		return 0, classify(err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if writer.Attrs().Generation <= generation {
		return 0, errors.New("policy-log write did not advance object generation")
	}
	return writer.Attrs().Generation, nil
}

func classify(err error) error {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return objectstore.ErrNotFound
	}
	var apiError *googleapi.Error
	if errors.As(err, &apiError) && apiError.Code == 412 {
		return objectstore.ErrPrecondition
	}
	return fmt.Errorf("policy-log GCS: %w", err)
}
