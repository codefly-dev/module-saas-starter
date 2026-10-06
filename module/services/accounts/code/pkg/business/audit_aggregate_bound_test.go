package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// aggregatingStore is a store of record whose aggregation is whatever the test
// says.
type aggregatingStore struct {
	AuditStore
	buckets []AuditAggregateBucket
	err     error
}

func (s aggregatingStore) AggregateAuditEvents(context.Context, AuditRead, AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	return s.buckets, s.err
}

// A store that gives up an aggregation for its size is a request the caller can
// narrow, not a server fault: the refusal reaches the caller as such, with the
// same code an export's does, and no bucket comes with it.
func TestAuditAggregateTooLargeIsARefusalNotAFault(t *testing.T) {
	service := &Service{auditStore: aggregatingStore{
		buckets: []AuditAggregateBucket{{Key: "partial", Count: 1}},
		err:     fmt.Errorf("read: %w", ErrAuditAggregateTooLarge),
	}}
	buckets, err := service.AggregateAuditLog(t.Context(), AuditQuery{OrgID: "org-1"}, AuditAggregationSpec{})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code = %v (%v), want ResourceExhausted", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "narrow it by time range, actor or event type") {
		t.Errorf("the refusal says how to get past it, got %q", err)
	}
	if status.Code(err) != status.Code(exportRefusal(t)) {
		t.Errorf("an aggregation and an export refused for size carry different codes")
	}
	if len(buckets) != 0 {
		t.Errorf("a refused aggregation returned %d buckets", len(buckets))
	}
}

func exportRefusal(t *testing.T) error {
	t.Helper()
	service := &Service{auditStore: exportingStore{err: ErrAuditExportTooLarge}}
	_, _, _, err := service.ExportAuditLog(t.Context(), "org-1", "json", "", "", nil)
	return err
}

// Only that refusal is mapped: any other failure of the store is the caller's
// to see as it came, and an aggregation within the bound is served as before.
func TestAuditAggregateRefusalMapsOnlyItself(t *testing.T) {
	failure := errors.New("warehouse unreachable")
	service := &Service{auditStore: aggregatingStore{err: failure}}
	_, err := service.AggregateAuditLog(t.Context(), AuditQuery{OrgID: "org-1"}, AuditAggregationSpec{})
	if !errors.Is(err, failure) || status.Code(err) == codes.ResourceExhausted {
		t.Fatalf("err = %v, want the store's own failure", err)
	}

	want := []AuditAggregateBucket{{Key: "saas.auth.login", Count: 3}}
	service = &Service{auditStore: aggregatingStore{buckets: want}}
	got, err := service.AggregateAuditLog(t.Context(), AuditQuery{OrgID: "org-1"}, AuditAggregationSpec{})
	if err != nil || len(got) != 1 || got[0].Key != want[0].Key || got[0].Count != 3 {
		t.Fatalf("buckets = %v, err = %v; want %v", got, err, want)
	}
}
