package business_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// subscriptionStore answers the two journal reads and the access oracle the
// stream is built on; the transaction wrapper collapses to the caller's context.
type subscriptionStore struct {
	business.Store

	journal      []business.JournalEntry
	accessible   map[string]bool // "type|id|user" -> the subject may read it
	accessCalls  int
	journalCalls int
	journalErr   error
	orgIDs       []string
}

func newSubscriptionStore() *subscriptionStore {
	return &subscriptionStore{accessible: map[string]bool{}}
}

func (s *subscriptionStore) WithOrgTx(ctx context.Context, orgID string, fn func(context.Context) error) error {
	s.orgIDs = append(s.orgIDs, orgID)
	return fn(ctx)
}

// ListTenantJournal emulates the real keyset page: strictly after the cursor, in
// seq order, capped at limit. A fake that ignored either would let a paging bug
// through.
func (s *subscriptionStore) ListTenantJournal(_ context.Context, afterSeq int64, limit int) ([]business.JournalEntry, error) {
	s.journalCalls++
	if s.journalErr != nil {
		return nil, s.journalErr
	}
	page := make([]business.JournalEntry, 0, limit)
	for _, entry := range s.journal {
		if entry.Seq <= afterSeq {
			continue
		}
		if len(page) == limit {
			break
		}
		page = append(page, entry)
	}
	return page, nil
}

func (s *subscriptionStore) ResolveTenantJournalCursor(_ context.Context, eventID string) (int64, error) {
	for _, entry := range s.journal {
		if entry.EventID == eventID && eventID != "" {
			return entry.Seq, nil
		}
	}
	var head int64
	for _, entry := range s.journal {
		if entry.Seq > head {
			head = entry.Seq
		}
	}
	return head, nil
}

func (s *subscriptionStore) ListAccessibleResourceIDs(
	_ context.Context, _, subjectID string, _ gen.SubjectKind, resourceType, action string, candidates []string,
) ([]string, error) {
	s.accessCalls++
	if action != "read" {
		return nil, errors.New("unexpected action " + action)
	}
	var out []string
	for _, id := range candidates {
		if s.accessible[resourceType+"|"+id+"|"+subjectID] {
			out = append(out, id)
		}
	}
	return out, nil
}

const (
	streamOrg          = "org-1"
	streamUser         = "user-1"
	streamResourceType = "documents.entry"
	streamChanged      = "documents.entry.version_minted"
	streamRemoved      = "documents.entry.removed"
)

func streamService(t *testing.T, store business.Store) *business.Service {
	t.Helper()
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetFollowables([]business.FollowableResource{
		{ResourceType: streamResourceType, Events: []string{streamChanged, streamRemoved}},
	})
	return service
}

func journalEntry(seq int64, eventID, eventType, subject string) business.JournalEntry {
	return business.JournalEntry{
		Seq:       seq,
		EventID:   eventID,
		Type:      eventType,
		Subject:   subject,
		EventTime: time.Unix(seq, 0).UTC(),
	}
}

func (s *subscriptionStore) allow(resourceID, userID string) {
	s.accessible[streamResourceType+"|"+resourceID+"|"+userID] = true
}

func TestSubscriptionsDeliverADeclaredChangeToAnAuthorizedReader(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{
		journalEntry(1, "event-1", streamChanged, "entry-1"),
		journalEntry(2, "event-2", streamRemoved, "entry-1"),
	}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	events, cursor, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
	require.NoError(t, err)
	require.Equal(t, int64(2), cursor)
	require.Len(t, events, 2)
	require.Equal(t, streamChanged, events[0].Type)
	require.Equal(t, streamResourceType, events[0].ResourceType)
	require.Equal(t, "entry-1", events[0].ResourceID)
	require.Equal(t, "event-1", events[0].EventID)
	// Removal is not a host concept: it is another declared type over the same
	// resource, so the client learns which change it was from the type alone.
	require.Equal(t, streamRemoved, events[1].Type)
	require.Equal(t, []string{streamOrg}, store.orgIDs)
}

func TestSubscriptionsDeliverNothingForAResourceTheReaderMayNotSee(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{
		journalEntry(1, "event-1", streamChanged, "hidden-entry"),
		journalEntry(2, "event-2", streamChanged, "entry-1"),
	}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	events, cursor, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "entry-1", events[0].ResourceID)
	// The cursor clears the hidden entry too. Leaving it pending would re-run the
	// same denial on every poll and stall everything behind it.
	require.Equal(t, int64(2), cursor)
}

func TestSubscriptionsSkipAnEntryNoCompositionDeclared(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{
		journalEntry(1, "event-1", "saas.auth.login", "session-1"),
		journalEntry(2, "event-2", streamChanged, ""),
	}
	// Access is granted for both subjects: only the declaration and the subject
	// decide here, so a permissive oracle cannot rescue an entry the host cannot
	// resolve to a resource.
	store.accessible["saas.auth|session-1|"+streamUser] = true
	service := streamService(t, store)

	events, cursor, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
	require.NoError(t, err)
	require.Empty(t, events)
	require.Equal(t, int64(2), cursor)
	require.Zero(t, store.accessCalls, "an undeclared entry must not reach the access oracle")
}

func TestSubscriptionsResolveVisibilityOncePerResourceType(t *testing.T) {
	store := newSubscriptionStore()
	for seq := int64(1); seq <= 5; seq++ {
		store.journal = append(store.journal, journalEntry(seq, "event", streamChanged, "entry-1"))
	}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	events, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
	require.NoError(t, err)
	require.Len(t, events, 5)
	require.Equal(t, 1, store.accessCalls)
}

func TestSubscriptionsResumeAfterTheCursorAndHoldItOnFailure(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{
		journalEntry(1, "event-1", streamChanged, "entry-1"),
		journalEntry(2, "event-2", streamChanged, "entry-1"),
		journalEntry(3, "event-3", streamChanged, "entry-1"),
	}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	events, cursor, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 1)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "event-2", events[0].EventID)
	require.Equal(t, int64(3), cursor)

	store.journalErr = errors.New("journal unavailable")
	_, held, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, cursor)
	require.Error(t, err)
	require.Equal(t, cursor, held, "a failed read must not advance past entries it never delivered")
}

func TestSubscriptionCursorResumesFromTheLastEventAndOtherwiseFromTheHead(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{
		journalEntry(1, "event-1", streamChanged, "entry-1"),
		journalEntry(2, "event-2", streamChanged, "entry-1"),
	}
	service := streamService(t, store)

	resumed, err := service.OpenSubscriptionCursor(context.Background(), streamOrg, "event-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), resumed)

	// No cursor, and a cursor the tenant cannot resolve, are the same answer: the
	// tenant's head. An id from another organization must not be distinguishable
	// from one that never existed.
	for _, presented := range []string{"", "event-from-another-tenant"} {
		head, err := service.OpenSubscriptionCursor(context.Background(), streamOrg, presented)
		require.NoError(t, err)
		require.Equal(t, int64(2), head)
	}
}
