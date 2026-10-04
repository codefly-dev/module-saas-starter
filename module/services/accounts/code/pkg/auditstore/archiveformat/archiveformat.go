// Package archiveformat is the one shape of the audit store swap's locked
// archive (ADR 0009), whichever object store holds it: one JSON Lines object
// per relay batch, named by the batch, full for security-class events and
// content-free for content-class ones. Every archive writer (gcsarchive,
// s3archive) names and encodes its objects here, so an archive reader reads
// every bucket the same way, and moving a deployment between object stores
// changes where the objects are, never what they say.
package archiveformat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"accounts/pkg/business"
)

// ContentType is every archive object's media type.
const ContentType = "application/x-ndjson"

// ObjectName names a batch's object:
//
//	<deployment>/<yyyy>/<mm>/<dd>/<yyyymmddThhmmss.nnnnnnnnnZ>-<batch id>.jsonl
//
// The date path is when the relay composed the batch, so a day's objects list
// together and in delivery order. The batch id is unique to one delivery
// attempt: a retried batch is a new object, never a rewrite of an old one.
func ObjectName(batch business.AuditBatch) (string, error) {
	if !safeSegment(batch.DeploymentID) || !safeSegment(batch.ID) {
		return "", fmt.Errorf("audit archive: deployment id %q and batch id %q must be plain path segments", batch.DeploymentID, batch.ID)
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
			return nil, fmt.Errorf("audit archive: encode event %s: %w", entry.ID, err)
		}
	}
	return buf.Bytes(), nil
}
