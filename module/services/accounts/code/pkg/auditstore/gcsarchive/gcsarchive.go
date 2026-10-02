// Package gcsarchive writes the audit store swap's locked archive (ADR 0009) to
// a Google Cloud Storage bucket: one JSON Lines object per relay batch.
//
// The writer's authority is storage.objects.create alone. It never overwrites —
// every write carries a does-not-exist precondition — and never deletes; the
// bucket's retention lock (GCS Bucket Lock), which the deployment configures,
// is what keeps anyone else from doing either before the window ends.
// Credentials are Application Default Credentials only: the caller builds the
// client, and on Kubernetes that is the pod's workload identity.
package gcsarchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"strings"
	"time"

	"accounts/pkg/business"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

// ContentType is every archive object's media type.
const ContentType = "application/x-ndjson"

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
	name, err := ObjectName(batch)
	if err != nil {
		return err
	}
	body, err := Encode(batch)
	if err != nil {
		return err
	}
	if err := a.bucket.CreateObject(ctx, name, body); err != nil {
		return fmt.Errorf("gcs audit archive: write %s: %w", name, err)
	}
	return nil
}

// ObjectName names a batch's object:
//
//	<deployment>/<yyyy>/<mm>/<dd>/<yyyymmddThhmmss.nnnnnnnnnZ>-<batch id>.jsonl
//
// The date path is when the relay composed the batch, so a day's objects list
// together and in delivery order. The batch id is unique to one delivery
// attempt: a retried batch is a new object, never a rewrite of an old one.
func ObjectName(batch business.AuditBatch) (string, error) {
	if !safeSegment(batch.DeploymentID) || !safeSegment(batch.ID) {
		return "", fmt.Errorf("gcs audit archive: deployment id %q and batch id %q must be plain path segments", batch.DeploymentID, batch.ID)
	}
	at := batch.ComposedAt.UTC()
	return fmt.Sprintf("%s/%s/%s-%s.jsonl",
		batch.DeploymentID, at.Format("2006/01/02"), at.Format("20060102T150405.000000000Z"), batch.ID), nil
}

func safeSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, r := range segment {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// Line is one archive line: an event's envelope, the SHA-256 of its canonical
// details, and — for a security-class event only — the details themselves. A
// content-class line is content-free. Details is the canonical string as the
// store keeps it, so a reader verifies a line by hashing that string.
type Line struct {
	EventID        string `json:"event_id"`
	DeploymentID   string `json:"deployment_id"`
	BatchID        string `json:"batch_id"`
	OrgID          string `json:"org_id,omitempty"`
	ActorID        string `json:"actor_id,omitempty"`
	ActorType      string `json:"actor_type"`
	EventType      string `json:"event_type"`
	SchemaVersion  int    `json:"schema_version"`
	Resource       string `json:"resource"`
	ResourceID     string `json:"resource_id,omitempty"`
	OccurredAt     string `json:"occurred_at"`
	IPAddress      string `json:"ip_address,omitempty"`
	ImpersonatedBy string `json:"impersonated_by,omitempty"`
	IsImpersonated bool   `json:"is_impersonated"`
	ClientID       string `json:"client_id,omitempty"`
	RetentionClass string `json:"retention_class"`
	DetailsSHA256  string `json:"details_sha256"`
	Details        string `json:"details,omitempty"`
}

// Encode renders a batch as JSON Lines, one line per record in batch order.
func Encode(batch business.AuditBatch) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	for _, record := range batch.Records {
		entry := record.Entry
		line := Line{
			EventID:        entry.ID,
			DeploymentID:   batch.DeploymentID,
			BatchID:        batch.ID,
			OrgID:          entry.OrgID,
			ActorID:        entry.ActorID,
			ActorType:      entry.ActorType,
			EventType:      string(entry.EventType),
			SchemaVersion:  entry.SchemaVersion,
			Resource:       entry.Resource,
			ResourceID:     entry.ResourceID,
			OccurredAt:     entry.CreatedAt.UTC().Format(time.RFC3339Nano),
			IPAddress:      entry.IPAddress,
			ImpersonatedBy: entry.ImpersonatedBy,
			IsImpersonated: entry.IsImpersonated,
			ClientID:       entry.ClientID,
			RetentionClass: string(record.Retention),
			DetailsSHA256:  record.DetailsSHA256,
		}
		if record.Retention == business.RetentionSecurity {
			line.Details = record.Details
		}
		if err := encoder.Encode(line); err != nil {
			return nil, fmt.Errorf("gcs audit archive: encode event %s: %w", entry.ID, err)
		}
	}
	return buf.Bytes(), nil
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
	writer.ContentType = ContentType
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
