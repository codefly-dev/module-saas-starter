package business

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
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

	raw, err := auditToCSV(entries)
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

	encoded, err := auditToJSON(entries)
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
