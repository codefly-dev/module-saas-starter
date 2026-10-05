package business

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The downloadable export is the surface a compliance review actually leaves
// with, and both of its shapes project the entry field by field — so a column
// the store gained is dropped here silently, and the trail then answers "what
// did they do it through" in the UI and over the API but not in the file.
func TestAuditExportCarriesTheClientTheCallCameThrough(t *testing.T) {
	entries := []AuditEntry{
		{
			ID: "evt-client", EventType: EventTeamCreated, SchemaVersion: 1,
			ActorID: "user-1", ActorType: ActorTypeUser, Resource: "team", ResourceID: "team-1",
			OrgID: "org-1", ClientID: "example-console", CreatedAt: time.Unix(0, 0).UTC(),
		},
		{
			ID: "evt-web", EventType: EventTeamCreated, SchemaVersion: 1,
			ActorID: "user-1", ActorType: ActorTypeUser, Resource: "team", ResourceID: "team-2",
			OrgID: "org-1", CreatedAt: time.Unix(0, 0).UTC(),
		},
	}

	raw, err := auditToCSV(t.Context(), NewAuditEventResolver(nil), entries)
	if err != nil {
		t.Fatalf("auditToCSV: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want header + 2", len(rows))
	}
	column := -1
	for i, name := range rows[0] {
		if name == "client_id" {
			column = i
		}
	}
	if column < 0 {
		t.Fatalf("csv header has no client_id column: %v", rows[0])
	}
	// Every row must have a cell for it, so the header and the rows cannot
	// drift into naming one column and writing another.
	if got := rows[1][column]; got != "example-console" {
		t.Errorf("client row client_id = %q, want example-console", got)
	}
	if got := rows[2][column]; got != "" {
		t.Errorf("web-session row client_id = %q, want empty", got)
	}

	encoded, err := auditToJSON(t.Context(), NewAuditEventResolver(nil), entries)
	if err != nil {
		t.Fatalf("auditToJSON: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if got := decoded[0]["client_id"]; got != "example-console" {
		t.Errorf("json client_id = %v, want example-console", got)
	}
	if _, present := decoded[1]["client_id"]; present {
		t.Errorf("a call from the host's own session must name no client, got %v", decoded[1]["client_id"])
	}
}

// exportingStore is a store of record whose export is whatever the test says.
type exportingStore struct {
	AuditStore
	entries []AuditEntry
	err     error
}

func (s exportingStore) ExportAuditEvents(context.Context, AuditRead) ([]AuditEntry, error) {
	return s.entries, s.err
}

// A store that gives up an export for its size is a request the caller can
// narrow, not a server fault: the refusal reaches the caller as such, and an
// export within the bound is served as before.
func TestAuditExportTooLargeIsARefusalNotAFault(t *testing.T) {
	service := &Service{auditStore: exportingStore{err: fmt.Errorf("read: %w", ErrAuditExportTooLarge)}}
	_, _, _, err := service.ExportAuditLog(t.Context(), "org-1", "json", "", "", nil)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code = %v (%v), want ResourceExhausted", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "narrow it by actor or event type") {
		t.Errorf("the refusal says how to get past it, got %q", err)
	}

	service = &Service{auditStore: exportingStore{entries: []AuditEntry{
		{ID: "evt-1", EventType: EventTeamCreated, SchemaVersion: 1, OrgID: "org-1", CreatedAt: time.Unix(0, 0).UTC()},
	}}}
	body, contentType, _, err := service.ExportAuditLog(t.Context(), "org-1", "json", "", "", nil)
	if err != nil {
		t.Fatalf("ExportAuditLog: %v", err)
	}
	if contentType != "application/json" || !strings.Contains(string(body), "evt-1") {
		t.Errorf("export = %s (%s)", body, contentType)
	}
}
