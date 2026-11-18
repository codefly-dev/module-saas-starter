package business

import (
	"accounts/pkg/auth"
	"context"
	"errors"
	"slices"
	"testing"
)

type failingSessionMarkers struct {
	auth.JWTMinter
	keys []string
	fail bool
}

func (m *failingSessionMarkers) RevokeSessionAccess(_ context.Context, key string) error {
	m.keys = append(m.keys, key)
	if m.fail && key == "session" {
		return errors.New("marker store unavailable")
	}
	return nil
}
func TestSessionMarkerFailureIsReportedAndDoesNotSkipFamily(t *testing.T) {
	m := &failingSessionMarkers{fail: true}
	s := &Service{minter: m}
	rows := []RevokedSession{{ID: "session", FamilyID: "family"}}
	if err := s.publishSessionRevocations(context.Background(), rows); !errors.Is(err, ErrSessionEndIncomplete) {
		t.Fatalf("missing incomplete-containment error: %v", err)
	}
	if !slices.Contains(m.keys, "family") {
		t.Fatal("failed session marker skipped family marker")
	}
	m.fail = false
	m.keys = nil
	if err := s.publishSessionRevocations(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.keys, "session") || !slices.Contains(m.keys, "family") {
		t.Fatalf("retry omitted markers: %v", m.keys)
	}
}

type sessionLedgerStore struct {
	Store
	rows    []RevokedSession
	err     error
	checked string
}

func (s *sessionLedgerStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *sessionLedgerStore) CheckCapabilitySession(_ context.Context, id string) error {
	s.checked = id
	return s.err
}
func (s *sessionLedgerStore) RevokedSessionMarkers(context.Context) ([]RevokedSession, error) {
	return s.rows, s.err
}

func TestCapabilitySessionLookupFailsClosed(t *testing.T) {
	failure := errors.New("ledger unavailable")
	store := &sessionLedgerStore{err: failure}
	service := &Service{store: store}
	if err := service.CheckCapabilitySession(context.Background(), "session"); !errors.Is(err, failure) {
		t.Fatalf("lookup failure lost: %v", err)
	}
	if store.checked != "session" {
		t.Fatalf("checked %q", store.checked)
	}
	store.err = nil
	if err := service.CheckCapabilitySession(context.Background(), "session"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRevocationReconciliationRetriesDurableRows(t *testing.T) {
	store := &sessionLedgerStore{rows: []RevokedSession{{ID: "session", FamilyID: "family"}}}
	markers := &failingSessionMarkers{fail: true}
	service := &Service{store: store, minter: markers}
	if err := service.ReconcileSessionRevocations(context.Background()); !errors.Is(err, ErrSessionEndIncomplete) {
		t.Fatalf("first pass: %v", err)
	}
	// A new service models a restart: only the durable store survives.
	markers = &failingSessionMarkers{}
	service = &Service{store: store, minter: markers}
	if err := service.ReconcileSessionRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(markers.keys, "session") || !slices.Contains(markers.keys, "family") {
		t.Fatalf("restart omitted markers: %v", markers.keys)
	}
	store.err = errors.New("ledger unavailable")
	markers.keys = nil
	if err := service.ReconcileSessionRevocations(context.Background()); !errors.Is(err, store.err) {
		t.Fatalf("lookup failure lost: %v", err)
	}
	if len(markers.keys) != 0 {
		t.Fatal("published after failed read")
	}
}
