package business_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

	journal       []business.JournalEntry
	payloads      map[string]business.JournalPayload
	accessible    map[string]bool // "type|id|user" -> the subject may read it
	accessCalls   int
	journalCalls  int
	payloadCalls  int
	payloadIDs    []string
	journalErr    error
	orgIDs        []string
	journalOrgIDs []string
}

func newSubscriptionStore() *subscriptionStore {
	return &subscriptionStore{
		accessible: map[string]bool{},
		payloads:   map[string]business.JournalPayload{},
	}
}

func (s *subscriptionStore) WithOrgTx(ctx context.Context, orgID string, fn func(context.Context) error) error {
	s.orgIDs = append(s.orgIDs, orgID)
	return fn(ctx)
}

// ListTenantJournal emulates the real keyset page: strictly after the cursor, in
// seq order, capped at limit. A fake that ignored either would let a paging bug
// through.
func (s *subscriptionStore) ListTenantJournal(_ context.Context, orgID string, afterSeq int64, limit int) ([]business.JournalEntry, error) {
	s.journalCalls++
	s.journalOrgIDs = append(s.journalOrgIDs, orgID)
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

func (s *subscriptionStore) ResolveTenantJournalCursor(_ context.Context, _, eventID string) (int64, bool, error) {
	for _, entry := range s.journal {
		if entry.EventID == eventID && eventID != "" {
			return entry.Seq, true, nil
		}
	}
	var head int64
	for _, entry := range s.journal {
		if entry.Seq > head {
			head = entry.Seq
		}
	}
	return head, false, nil
}

func (s *subscriptionStore) LoadTenantJournalPayloads(
	_ context.Context, _ string, eventIDs []string, maxBytes int,
) (map[string]business.JournalPayload, error) {
	s.payloadCalls++
	s.payloadIDs = append(s.payloadIDs, eventIDs...)
	out := map[string]business.JournalPayload{}
	for _, id := range eventIDs {
		payload, ok := s.payloads[id]
		if !ok {
			continue
		}
		if len(payload.Data) > maxBytes {
			continue
		}
		out[id] = payload
	}
	return out, nil
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

	events, cursor, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
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

	events, cursor, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
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

	events, cursor, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
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

	events, _, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
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

	events, cursor, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 1)
	require.NoError(t, err)
	// The window starts below the cursor, so the entry already delivered is
	// offered again; the reader drops the repeat. That re-read is what recovers a
	// transaction that committed after the cursor passed its seq.
	require.Len(t, events, 3)
	require.Equal(t, "event-1", events[0].EventID)
	require.Equal(t, "event-2", events[1].EventID)
	require.Equal(t, int64(3), cursor)

	store.journalErr = errors.New("journal unavailable")
	_, held, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, cursor)
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

	resumed, resolved, err := service.OpenSubscriptionCursor(context.Background(), streamOrg, "event-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), resumed)
	require.True(t, resolved)

	// No cursor, and a cursor the tenant cannot resolve, are the same answer: the
	// tenant's head. An id from another organization must not be distinguishable
	// from one that never existed.
	for _, presented := range []string{"", "event-from-another-tenant"} {
		head, resolved, err := service.OpenSubscriptionCursor(context.Background(), streamOrg, presented)
		require.NoError(t, err)
		require.Equal(t, int64(2), head)
		require.False(t, resolved, "an unresolved cursor must be reported so the reader is told its history was skipped")
	}
}

// seq is taken at INSERT and the row becomes visible at COMMIT, so a producer
// can hold a low seq and commit after a higher one is already visible. A reader
// that trusted seq as a visibility frontier would advance past it and never look
// back. The window below the cursor is what makes that entry recoverable.
func TestSubscriptionsReReadTheWindowBelowTheCursor(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{journalEntry(9, "event-fast", streamChanged, "entry-1")}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	events, cursor, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, int64(9), cursor)

	// The slow producer's entry lands BELOW the cursor once its transaction
	// commits. A high-water mark would never see it again.
	store.journal = append([]business.JournalEntry{journalEntry(8, "event-slow", streamChanged, "entry-1")}, store.journal...)

	recovered, _, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, cursor)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, event := range recovered {
		ids[event.EventID] = true
	}
	require.True(t, ids["event-slow"], "an entry that committed below the cursor must still be delivered")
}

// The cursor must never move backwards: the window starts below it, so a short
// page ends behind it, and following the page blindly would re-read forever.
func TestSubscriptionCursorNeverMovesBackwards(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{journalEntry(3, "event-3", streamChanged, "entry-1")}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	_, cursor, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 40)
	require.NoError(t, err)
	require.Equal(t, int64(40), cursor)
}

// The page is read before anyone knows what the caller may see, so reading
// payloads with it would charge a reader with no access for every byte in the
// tenant. domain_events.data carries no size CHECK to bound that.
func TestSubscriptionPayloadsAreReadOnlyForVisibleEntries(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{
		journalEntry(1, "event-hidden", streamChanged, "hidden-entry"),
		journalEntry(2, "event-visible", streamChanged, "entry-1"),
	}
	store.payloads["event-hidden"] = business.JournalPayload{ContentType: "application/json", Data: []byte(`{"secret":1}`)}
	store.payloads["event-visible"] = business.JournalPayload{ContentType: "application/json", Data: []byte(`{"v":7}`)}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	events, _, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.JSONEq(t, `{"v":7}`, string(events[0].Data))
	require.False(t, events[0].DataOmitted)
	require.Equal(t, []string{"event-visible"}, store.payloadIDs,
		"a payload must never be read for an entry the caller may not see")
}

// An oversized payload is reported as omitted rather than dropped silently: with
// no flag it is indistinguishable from an entry that carries none, and a client
// could not tell that re-reading from the owner would get it more.
func TestSubscriptionMarksAnOversizedPayloadOmitted(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{journalEntry(1, "event-1", streamChanged, "entry-1")}
	store.payloads["event-1"] = business.JournalPayload{
		ContentType: "application/json",
		Data:        []byte(`{"v":"` + strings.Repeat("x", 128*1024) + `"}`),
	}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	events, _, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].DataOmitted)
	require.Empty(t, events[0].Data)
}

// A page that does not exhaust what is waiting reports more, and the cursor
// still advances past an entry it delivered — a budget spent entirely on
// re-reads below the cursor would otherwise stall the stream on the same page
// forever.
func TestSubscriptionTruncationAlwaysAdvancesTheCursor(t *testing.T) {
	store := newSubscriptionStore()
	for seq := int64(1); seq <= 120; seq++ {
		store.journal = append(store.journal, journalEntry(seq, fmt.Sprintf("event-%d", seq), streamChanged, "entry-1"))
	}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	cursor := int64(60)
	events, next, more, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, cursor)
	require.NoError(t, err)
	require.True(t, more)
	require.Greater(t, next, cursor, "a truncated page must still move the cursor or the stream stalls")
	require.NotEmpty(t, events)

	// And it converges: repeating from the returned cursor makes progress every
	// time rather than looping on the same window.
	for i := 0; i < 10 && more; i++ {
		previous := next
		_, next, more, err = service.ReadSubscriptions(context.Background(), streamOrg, streamUser, next)
		require.NoError(t, err)
		require.GreaterOrEqual(t, next, previous)
		if next == previous {
			require.False(t, more, "no progress must mean no more work, or the reader spins")
		}
	}
}

// Both journal reads pin the org themselves on top of the RLS floor, so a caller
// that ever reached them with RLS bypassed still reads one tenant.
func TestSubscriptionJournalReadPinsTheOrganization(t *testing.T) {
	store := newSubscriptionStore()
	store.journal = []business.JournalEntry{journalEntry(1, "event-1", streamChanged, "entry-1")}
	store.allow("entry-1", streamUser)
	service := streamService(t, store)

	_, _, _, err := service.ReadSubscriptions(context.Background(), streamOrg, streamUser, 0)
	require.NoError(t, err)
	require.Equal(t, []string{streamOrg}, store.journalOrgIDs)
}
