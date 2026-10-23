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
	"time"

	"policy-log/objectstore"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

var entryName = regexp.MustCompile(`^entries/[0-9]{20}\.json$`)

// GCS owns a bucket in the witness trust domain, never a host database or a
// host-controlled bucket. It performs one attempt per write, even for 5xx.
type GCS struct {
	client    *storage.Client
	bucket    *storage.BucketHandle
	retention time.Duration
	now       func() time.Time
}

// MaxRetention bounds the delivered entry retention. A Locked retention cannot
// be shortened or removed by anyone, so a typo of an extra digit would hold the
// evidence — and the bucket — for a century. Ten years covers every declared
// audit horizon with room to spare.
const MaxRetention = 10 * 366 * 24 * time.Hour

// New uses the service's workload identity and the official GCS endpoint. There
// is no endpoint or emulator fallback in production. Bucket identity is delivery
// data; IAM and the service's own workload identity enforce its trust domain.
//
// retention is how long every entry is held under a LOCKED object retention:
// once written, nobody — not this service, not a project owner — can delete or
// replace it before then. That is the "retention" half of the immutable
// prefix; the bucket's IAM, which lets this identity create entries and never
// delete them, is the other. A bucket without object retention enabled refuses
// the write, so a store that cannot hold the lock refuses every append rather
// than accepting evidence it cannot protect.
func New(ctx context.Context, bucket string, retention time.Duration) (*GCS, error) {
	if bucket == "" {
		return nil, errors.New("policy-log: GCS bucket is required")
	}
	if retention <= 0 || retention > MaxRetention {
		return nil, fmt.Errorf("policy-log: entry retention must be positive and at most %s", MaxRetention)
	}
	if os.Getenv("STORAGE_EMULATOR_HOST") != "" {
		return nil, errors.New("policy-log: storage emulator is forbidden")
	}
	client, err := storage.NewClient(ctx, storage.WithJSONReads())
	if err != nil {
		return nil, err
	}
	return newGCS(client, bucket, retention), nil
}

func newGCS(client *storage.Client, bucket string, retention time.Duration) *GCS {
	return &GCS{client: client, bucket: client.Bucket(bucket).Retryer(storage.WithPolicy(storage.RetryNever)),
		retention: retention, now: time.Now}
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
	if name != objectstore.HeadObject {
		// The head is the one object rewritten on every append; an entry is never
		// rewritten, so it is locked for the declared horizon as it is created.
		writer.Retention = &storage.ObjectRetention{Mode: "Locked", RetainUntil: s.now().Add(s.retention)}
	}
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
