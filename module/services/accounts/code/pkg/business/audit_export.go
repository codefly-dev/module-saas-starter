package business

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuditExportMaxBytes bounds the events one export of a store of record
// holds: the text of their envelopes and details, 32 MiB — some tens of
// thousands of events. The export is one unary response, so the whole of it
// is in memory twice over (the events, then the file serialized from them)
// however the store reads it; without a bound a long history is a pod's
// memory, and the pod is every tenant's. A store reads in windows and gives
// up as soon as the events it has gathered pass the bound.
const AuditExportMaxBytes = 32 << 20

// ErrAuditExportTooLarge is the refusal of an export whose events pass
// AuditExportMaxBytes. Narrowing the export (an actor, an event type) is the
// way past it.
var ErrAuditExportTooLarge = errors.New("audit export: more events match than one download holds; narrow it by actor or event type")

// ExportAuditLog queries all matching audit events and serializes them to CSV or
// JSON. eventTypes is the set form of eventType, applied together with it, so a
// download can carry the same family-of-types filter a summary tile opens.
func (s *Service) ExportAuditLog(ctx context.Context, orgID, format, actorID, eventType string, eventTypes []string) ([]byte, string, string, error) {
	w := wool.Get(ctx).In("ExportAuditLog")

	if format == "" {
		format = "json"
	}
	if format != "csv" && format != "json" {
		return nil, "", "", w.NewError("unsupported format: %s (use csv or json)", format)
	}

	// Every matching event, newest first, from the audit store of record. In
	// Postgres the export pages the list 100 rows at a time, each page under
	// WithOrgTx (or WithControlPlane when orgID is empty for platform-admin
	// export) so RLS lets the rows through.
	read, err := s.auditRead(ctx, AuditQuery{
		OrgID: orgID, ActorID: actorID, EventType: eventType, EventTypes: eventTypes,
	}, nil)
	if err != nil {
		return nil, "", "", w.Wrapf(err, "query audit log for export")
	}
	all, err := s.auditReads().ExportAuditEvents(ctx, read)
	if errors.Is(err, ErrAuditExportTooLarge) {
		return nil, "", "", status.Error(codes.ResourceExhausted, ErrAuditExportTooLarge.Error())
	}
	if err != nil {
		return nil, "", "", w.Wrapf(err, "query audit log for export")
	}

	timestamp := time.Now().UTC().Format("20060102-150405")

	switch format {
	case "csv":
		data, err := auditToCSV(ctx, s.AuditEventResolver(), all)
		if err != nil {
			return nil, "", "", w.Wrapf(err, "serialize audit CSV")
		}
		return data, "text/csv", fmt.Sprintf("audit-log-%s.csv", timestamp), nil
	default:
		data, err := auditToJSON(ctx, s.AuditEventResolver(), all)
		if err != nil {
			return nil, "", "", w.Wrapf(err, "serialize audit JSON")
		}
		return data, "application/json", fmt.Sprintf("audit-log-%s.json", timestamp), nil
	}
}

func auditToCSV(ctx context.Context, resolver *AuditEventResolver, entries []AuditEntry) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	// Header
	if err := writer.Write([]string{"id", "event_type", "schema_version", "category", "actor_id", "actor_type", "resource", "resource_id", "org_id", "ip_address", "client_id", "created_at"}); err != nil {
		return nil, err
	}

	for _, e := range entries {
		resolved, err := resolver.Resolve(ctx, e.EventType)
		if err != nil {
			return nil, err
		}
		category := resolved.Category()
		if err := writer.Write([]string{
			e.ID,
			string(e.EventType),
			strconv.Itoa(e.SchemaVersion),
			category,
			e.ActorID,
			e.ActorType,
			e.Resource,
			e.ResourceID,
			e.OrgID,
			e.IPAddress,
			e.ClientID,
			e.CreatedAt.Format(time.RFC3339),
		}); err != nil {
			return nil, err
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type auditExportEntry struct {
	ID            string         `json:"id"`
	EventType     string         `json:"event_type"`
	SchemaVersion int            `json:"schema_version"`
	Category      string         `json:"category,omitempty"`
	ActorID       string         `json:"actor_id"`
	ActorType     string         `json:"actor_type"`
	Resource      string         `json:"resource"`
	ResourceID    string         `json:"resource_id"`
	OrgID         string         `json:"org_id"`
	IPAddress     string         `json:"ip_address"`
	ClientID      string         `json:"client_id,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

func auditToJSON(ctx context.Context, resolver *AuditEventResolver, entries []AuditEntry) ([]byte, error) {
	out := make([]auditExportEntry, len(entries))
	for i, e := range entries {
		resolved, err := resolver.Resolve(ctx, e.EventType)
		if err != nil {
			return nil, err
		}
		out[i] = auditEntryToExport(e, resolved)
	}
	return json.MarshalIndent(out, "", "  ")
}

// auditEntryToExport is the shared export projection for both the CSV/JSON
// download and the S3 JSONL exporter. Payloads are PII-redacted here because
// every caller writes to a destination outside the audit store.
func auditEntryToExport(e AuditEntry, resolved ResolvedAuditEvent) auditExportEntry {
	category := resolved.Category()
	return auditExportEntry{
		ID:            e.ID,
		EventType:     string(e.EventType),
		SchemaVersion: e.SchemaVersion,
		Category:      category,
		ActorID:       e.ActorID,
		ActorType:     e.ActorType,
		Resource:      e.Resource,
		ResourceID:    e.ResourceID,
		OrgID:         e.OrgID,
		IPAddress:     e.IPAddress,
		ClientID:      e.ClientID,
		Payload:       resolved.Redact(e.Payload),
		CreatedAt:     e.CreatedAt.Format(time.RFC3339),
	}
}
