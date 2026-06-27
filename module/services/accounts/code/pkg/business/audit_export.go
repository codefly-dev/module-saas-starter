package business

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
)

// ExportAuditLog queries all matching audit events and serializes them to CSV or JSON.
func (s *Service) ExportAuditLog(ctx context.Context, orgID, format, actorID, action string) ([]byte, string, string, error) {
	w := wool.Get(ctx).In("ExportAuditLog")

	if format == "" {
		format = "json"
	}
	if format != "csv" && format != "json" {
		return nil, "", "", w.NewError("unsupported format: %s (use csv or json)", format)
	}

	// Fetch all matching events (paginate internally to avoid huge single queries)
	var all []AuditEntry
	pageToken := ""
	for {
		// Service.QueryAuditLog wraps in WithOrgTx (or WithBypass when
		// orgID is empty for platform-admin export) so RLS lets the
		// rows through. Don't use s.store.QueryAuditLog directly here
		// — that bypasses the wrap and returns zero rows.
		entries, nextToken, _, err := s.QueryAuditLog(ctx, orgID, actorID, action, "", "", nil, nil, 100, pageToken)
		if err != nil {
			return nil, "", "", w.Wrapf(err, "query audit log for export")
		}
		all = append(all, entries...)
		if nextToken == "" || len(entries) == 0 {
			break
		}
		pageToken = nextToken
	}

	timestamp := time.Now().UTC().Format("20060102-150405")

	switch format {
	case "csv":
		data, err := auditToCSV(all)
		if err != nil {
			return nil, "", "", w.Wrapf(err, "serialize audit CSV")
		}
		return data, "text/csv", fmt.Sprintf("audit-log-%s.csv", timestamp), nil
	default:
		data, err := auditToJSON(all)
		if err != nil {
			return nil, "", "", w.Wrapf(err, "serialize audit JSON")
		}
		return data, "application/json", fmt.Sprintf("audit-log-%s.json", timestamp), nil
	}
}

func auditToCSV(entries []AuditEntry) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	// Header
	if err := writer.Write([]string{"id", "actor_id", "actor_type", "action", "resource", "resource_id", "org_id", "ip_address", "created_at"}); err != nil {
		return nil, err
	}

	for _, e := range entries {
		if err := writer.Write([]string{
			e.ID,
			e.ActorID,
			e.ActorType,
			e.Action,
			e.Resource,
			e.ResourceID,
			e.OrgID,
			e.IPAddress,
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
	ID         string            `json:"id"`
	ActorID    string            `json:"actor_id"`
	ActorType  string            `json:"actor_type"`
	Action     string            `json:"action"`
	Resource   string            `json:"resource"`
	ResourceID string            `json:"resource_id"`
	OrgID      string            `json:"org_id"`
	IPAddress  string            `json:"ip_address"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  string            `json:"created_at"`
}

func auditToJSON(entries []AuditEntry) ([]byte, error) {
	out := make([]auditExportEntry, len(entries))
	for i, e := range entries {
		out[i] = auditExportEntry{
			ID:         e.ID,
			ActorID:    e.ActorID,
			ActorType:  e.ActorType,
			Action:     e.Action,
			Resource:   e.Resource,
			ResourceID: e.ResourceID,
			OrgID:      e.OrgID,
			IPAddress:  e.IPAddress,
			Metadata:   e.Metadata,
			CreatedAt:  e.CreatedAt.Format(time.RFC3339),
		}
	}
	return json.MarshalIndent(out, "", "  ")
}
