package business

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPAuditSinkDeliversJSON(t *testing.T) {
	entry := orgAuditEntry()
	var gotAuth string
	var gotBody auditExportPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sink, err := NewHTTPAuditSink(HTTPAuditSinkConfig{Endpoint: server.URL, Token: "tok", Client: server.Client()})
	if err != nil {
		t.Fatalf("NewHTTPAuditSink: %v", err)
	}
	if err := sink.Emit(t.Context(), entry); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", gotAuth)
	}
	if gotBody.ID != entry.ID || gotBody.OrgID != entry.OrgID {
		t.Fatalf("delivered body = %+v, want ID %s org %s", gotBody, entry.ID, entry.OrgID)
	}
}

func TestHTTPAuditSinkNon2xxIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	sink, err := NewHTTPAuditSink(HTTPAuditSinkConfig{Endpoint: server.URL, Client: server.Client()})
	if err != nil {
		t.Fatalf("NewHTTPAuditSink: %v", err)
	}
	if err := sink.Emit(t.Context(), orgAuditEntry()); err == nil {
		t.Fatal("Emit returned nil, want error on HTTP 500")
	}
}

func TestNewHTTPAuditSinkRejectsBadEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "not-a-url", "ftp://warehouse", "//no-scheme"} {
		if _, err := NewHTTPAuditSink(HTTPAuditSinkConfig{Endpoint: endpoint}); err == nil {
			t.Fatalf("NewHTTPAuditSink(%q) returned nil error, want rejection", endpoint)
		}
	}
}
