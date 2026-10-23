package business

import (
	"context"
	"errors"
	"testing"
	"time"
)

// scopeStore records which transaction a read ran in, and what the Store was
// asked, so PostgresAuditStorage's scoping is checked without a database.
type scopeStore struct {
	Store
	scopes      []string
	inserted    []AuditEntry
	queried     []AuditQuery
	aggregated  []AuditQuery
	insertError error
}

func (s *scopeStore) WithOrgTx(ctx context.Context, orgID string, fn func(context.Context) error) error {
	s.scopes = append(s.scopes, "org:"+orgID)
	return fn(ctx)
}

func (s *scopeStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	s.scopes = append(s.scopes, "control-plane")
	return fn(ctx)
}

func (s *scopeStore) InsertAuditEvent(_ context.Context, entry AuditEntry) error {
	s.inserted = append(s.inserted, entry)
	return s.insertError
}

func (s *scopeStore) QueryAuditLog(_ context.Context, q AuditQuery) ([]AuditEntry, string, int32, error) {
	s.queried = append(s.queried, q)
	return []AuditEntry{{ID: "one"}}, "next", 1, nil
}

func (s *scopeStore) AggregateAuditLog(_ context.Context, q AuditQuery, _ AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	s.aggregated = append(s.aggregated, q)
	return []AuditAggregateBucket{{Key: "k", Count: 2}}, nil
}

func TestAuditReadScopeNamesExactlyOneScope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope AuditReadScope
		ok    bool
	}{
		{"zero", AuditReadScope{}, false},
		{"organization", OrganizationAuditScope("org-1"), true},
		{"platform", PlatformAuditScope(), true},
		{"empty organization is the zero scope", OrganizationAuditScope(""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.scope.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrAuditReadUnscoped) {
				t.Fatalf("Validate = %v, want ErrAuditReadUnscoped", err)
			}
		})
	}
}

func TestAuditReadRefusesAQueryOutsideItsScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		read AuditRead
		ok   bool
	}{
		{"organization", AuditRead{Scope: OrganizationAuditScope("a"), Query: AuditQuery{OrgID: "a"}}, true},
		{"platform", AuditRead{Scope: PlatformAuditScope()}, true},
		{"another organization", AuditRead{Scope: OrganizationAuditScope("a"), Query: AuditQuery{OrgID: "b"}}, false},
		{"platform naming an organization", AuditRead{Scope: PlatformAuditScope(), Query: AuditQuery{OrgID: "a"}}, false},
		{"organization naming none", AuditRead{Scope: OrganizationAuditScope("a")}, false},
		{"uncompiled collection", AuditRead{Scope: OrganizationAuditScope("a"), Query: AuditQuery{OrgID: "a", CollectionID: "c"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.read.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate = %v, ok = %v", err, tc.ok)
			}
		})
	}
}

func TestAuditPageTokenRoundTrips(t *testing.T) {
	at := time.Date(2026, 10, 12, 8, 30, 0, 123456000, time.UTC)
	gotAt, gotID, err := DecodeAuditPageToken(EncodeAuditPageToken(at, "entry-1"))
	if err != nil || !gotAt.Equal(at) || gotID != "entry-1" {
		t.Fatalf("round trip = %v %q %v", gotAt, gotID, err)
	}
	if _, _, err := DecodeAuditPageToken("not base64 !"); err == nil {
		t.Fatal("a malformed token is refused")
	}
}

func TestPostgresAuditStorageReadsInTheScopesTransaction(t *testing.T) {
	store := &scopeStore{}
	storage, err := NewPostgresAuditStorage(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	orgRead := AuditRead{Scope: OrganizationAuditScope("org-1"), Query: AuditQuery{OrgID: "org-1"}}
	entries, next, err := storage.List(ctx, orgRead)
	if err != nil || len(entries) != 1 || next != "next" {
		t.Fatalf("List = %v %q %v", entries, next, err)
	}
	if _, err := storage.Aggregate(ctx, AuditRead{Scope: PlatformAuditScope()}, AuditAggregationSpec{}); err != nil {
		t.Fatal(err)
	}
	if got, want := store.scopes, []string{"org:org-1", "control-plane"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("transactions = %v, want %v", got, want)
	}

	// An unscoped read never opens a transaction, let alone reads.
	store.scopes, store.queried = nil, nil
	if _, _, err := storage.List(ctx, AuditRead{Query: AuditQuery{OrgID: "org-1"}}); !errors.Is(err, ErrAuditReadUnscoped) {
		t.Fatalf("unscoped List = %v, want ErrAuditReadUnscoped", err)
	}
	if len(store.scopes) != 0 || len(store.queried) != 0 {
		t.Fatalf("an unscoped read reached the store: %v %v", store.scopes, store.queried)
	}
}

func TestPostgresAuditStorageAppendWritesOnTheAmbientTransaction(t *testing.T) {
	store := &scopeStore{}
	storage, err := NewPostgresAuditStorage(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Append(t.Context(), AuditEntry{ID: "e-1", EventType: EventAuthLogin}); err != nil {
		t.Fatal(err)
	}
	if len(store.inserted) != 1 || store.inserted[0].ID != "e-1" {
		t.Fatalf("inserted = %v", store.inserted)
	}
	if len(store.scopes) != 0 {
		t.Fatalf("Append opened a transaction of its own: %v", store.scopes)
	}
	store.insertError = errors.New("boom")
	if err := storage.Append(t.Context(), AuditEntry{ID: "e-2"}); err == nil {
		t.Fatal("a failed insert is the caller's failure, so its transaction rolls back")
	}
}

func TestNewPostgresAuditStorageNeedsAStore(t *testing.T) {
	if _, err := NewPostgresAuditStorage(nil); err == nil {
		t.Fatal("a storage over no store is refused")
	}
}
