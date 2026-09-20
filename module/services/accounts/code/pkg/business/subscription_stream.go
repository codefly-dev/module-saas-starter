package business

import (
	"context"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The subscriptions stream is the live companion to the follow bridge
// (FOLLOWS.md): the same committed journal, the same declared followable
// resources and the same access oracle, read forward by a connected client
// instead of fanned out into the inbox. A follow is a durable intent to be told
// later; this is a cursor a client holds now, and it asks for no follow row.
//
// The host stays owner-neutral here for the same reason the bridge does. What an
// event is about comes from the composed catalog's `follows:` declaration, the
// target instance from the envelope subject, and visibility from the generic
// (resource_type, resource_id) oracle — so two unrelated owning modules exercise
// identical code and this file names none of them.

// JournalEntry is one committed row of the tenant's journal: the event's
// identity and what it is about. The producer's payload is fetched separately,
// after visibility, and never read for an entry the caller may not see.
type JournalEntry struct {
	Seq       int64
	EventID   string
	Type      string
	Subject   string
	EventTime time.Time
}

// JournalPayload is one producer's own bytes, carried through unread.
type JournalPayload struct {
	ContentType string
	Data        []byte
}

// SubscriptionEvent is one journal entry the caller may currently see. Seq is
// server-side bookkeeping — it is how a reader prunes what it has delivered —
// and is never put on the wire: seq is a global identity column, so its gaps
// would disclose the platform's whole publish rate to one tenant.
type SubscriptionEvent struct {
	Seq             int64
	EventID         string
	Type            string
	ResourceType    string
	ResourceID      string
	Time            time.Time
	DataContentType string
	Data            []byte
	// DataOmitted marks an entry whose payload was too large to stream. The
	// alternative — sending the frame with no payload and no flag — is
	// indistinguishable from an entry that carries none, so a client could not
	// tell that re-reading from the owner would get it more.
	DataOmitted bool
}

const (
	// journalPageSize bounds one read of entry identities. A tenant's publish rate
	// is not the reader's to choose, so the stream advances a bounded page at a
	// time and comes straight back for the next one rather than materializing a
	// backlog.
	journalPageSize = 200

	// JournalRescanDepth is how far BELOW its cursor a reader re-reads on every
	// poll, and it is what makes the stream lossless rather than merely ordered.
	//
	// seq is taken at INSERT (an identity column) while the row becomes visible at
	// COMMIT, and publish_domain_event takes its serializing advisory lock only
	// for a non-empty partition — which a followable type must not declare. So a
	// producer that takes a low seq and commits late is invisible at a poll that
	// has already advanced past it, and a pure high-water mark would never look
	// back: the entry would sit in the journal with no cursor position that could
	// ever yield it. Re-reading a fixed depth catches it, at the cost of offering
	// an entry more than once, which is the at-least-once delivery this contract
	// already promises everywhere else. An entry is missed only if more journal
	// rows than this are committed between its own INSERT and its COMMIT.
	//
	// It must stay below journalPageSize or a page could be filled entirely by the
	// re-read and the cursor would stop advancing.
	JournalRescanDepth = 64

	// subscriptionEmitLimit bounds how many entries AHEAD of the cursor one poll
	// delivers. The cursor then stops at the last of them, so the remainder is the
	// next poll's work rather than a silent drop. One poll therefore reads at most
	// (subscriptionEmitLimit + JournalRescanDepth) payloads, each bounded by
	// subscriptionMaxPayloadBytes.
	subscriptionEmitLimit = 50

	// subscriptionMaxPayloadBytes is the largest producer payload carried inline.
	// domain_events.data has no size CHECK of its own, so an unbounded payload
	// would otherwise be read and framed in full, once per connected reader.
	subscriptionMaxPayloadBytes = 64 * 1024
)

// OpenSubscriptionCursor resolves where a stream starts: after the entry the
// client last received, or at the tenant's head when it presents none. resolved
// reports whether the presented id was found at all, so a reader whose cursor
// has aged out of retention can be told its history was skipped instead of
// silently concluding it is caught up.
func (s *Service) OpenSubscriptionCursor(ctx context.Context, orgID, lastEventID string) (cursor int64, resolved bool, err error) {
	err = s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var innerErr error
		cursor, resolved, innerErr = s.store.ResolveTenantJournalCursor(ctx, orgID, lastEventID)
		return innerErr
	})
	return cursor, resolved, err
}

// ReadSubscriptions returns the entries around and after cursor that the caller
// may see, the cursor to read from next, and whether more work is waiting.
//
// The window starts JournalRescanDepth BELOW the cursor, so an entry whose
// producing transaction committed after the cursor passed its seq is still
// delivered. Callers therefore see an entry more than once and drop the repeat;
// the alternative — trusting seq as a visibility frontier — loses it outright.
//
// The cursor advances over entries that were filtered out. An entry the caller
// may not see is not pending work: re-reading it forever would stall the stream
// behind it. Filtering leaves a gap in the delivered sequence and nothing else —
// hidden is indistinguishable from an entry that never concerned this reader.
func (s *Service) ReadSubscriptions(
	ctx context.Context, orgID, userID string, cursor int64,
) (events []SubscriptionEvent, next int64, more bool, err error) {
	next = cursor
	from := cursor - JournalRescanDepth
	if from < 0 {
		from = 0
	}

	err = s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		page, err := s.store.ListTenantJournal(ctx, orgID, from, journalPageSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}

		// Resolved once per entry: the declaration is read under a lock a later
		// SetFollowables could take between two passes, and one entry resolving to
		// two different resource types across those passes is a question nobody
		// should have to answer.
		type resolved struct {
			entry        JournalEntry
			resourceType string
		}
		declared := make([]resolved, 0, len(page))
		candidates := map[string][]string{}
		for _, entry := range page {
			resourceType, ok := s.subscribableResourceType(entry)
			if !ok {
				continue
			}
			declared = append(declared, resolved{entry: entry, resourceType: resourceType})
			candidates[resourceType] = append(candidates[resourceType], entry.Subject)
		}

		// One membership query per resource type on the page rather than a point
		// check per entry: the same grant-and-share union CheckAccess resolves, so
		// the stream and a per-record verdict can never disagree on an entry.
		allowed := map[string]map[string]struct{}{}
		for resourceType, ids := range candidates {
			accessible, err := s.store.ListAccessibleResourceIDs(ctx, orgID, userID,
				gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, resourceType, followVisibilityAction, ids)
			if err != nil {
				return err
			}
			set := make(map[string]struct{}, len(accessible))
			for _, id := range accessible {
				set[id] = struct{}{}
			}
			allowed[resourceType] = set
		}

		// The emit budget is spent on entries AHEAD of the cursor only, and every
		// visible re-read is emitted whatever the budget. Charging re-reads to the
		// budget would let a window full of them fill it without the cursor moving,
		// and the same page would then be re-read forever. Re-reads are bounded by
		// JournalRescanDepth, so one poll emits at most that many beyond the
		// budget.
		emittedAhead := 0
		lastAhead := int64(0)
		truncated := false
		for _, candidate := range declared {
			if _, ok := allowed[candidate.resourceType][candidate.entry.Subject]; !ok {
				continue
			}
			ahead := candidate.entry.Seq > cursor
			if ahead {
				if emittedAhead == subscriptionEmitLimit {
					truncated = true
					break
				}
				emittedAhead++
				lastAhead = candidate.entry.Seq
			}
			events = append(events, SubscriptionEvent{
				Seq:          candidate.entry.Seq,
				EventID:      candidate.entry.EventID,
				Type:         candidate.entry.Type,
				ResourceType: candidate.resourceType,
				ResourceID:   candidate.entry.Subject,
				Time:         candidate.entry.EventTime,
			})
		}

		switch {
		case truncated:
			// lastAhead is an entry past the cursor by construction, so the cursor
			// always moves and the remainder is the next poll's work.
			next = lastAhead
			more = true
		case page[len(page)-1].Seq > next:
			// The window starts below the cursor, so a short page can end behind it.
			// The cursor must never move backwards or the re-read repeats forever.
			next = page[len(page)-1].Seq
		}
		if len(page) == journalPageSize {
			more = true
		}

		return s.attachPayloads(ctx, orgID, events)
	})
	if err != nil {
		return nil, cursor, false, err
	}
	return events, next, more, nil
}

// attachPayloads reads the producer payloads of entries that already passed the
// visibility filter — never of the page, which is read before anyone knows what
// the caller may see.
func (s *Service) attachPayloads(ctx context.Context, orgID string, events []SubscriptionEvent) error {
	if len(events) == 0 {
		return nil
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.EventID)
	}
	payloads, err := s.store.LoadTenantJournalPayloads(ctx, orgID, ids, subscriptionMaxPayloadBytes)
	if err != nil {
		return err
	}
	for i := range events {
		payload, ok := payloads[events[i].EventID]
		if !ok {
			events[i].DataOmitted = true
			continue
		}
		events[i].DataContentType = payload.ContentType
		events[i].Data = payload.Data
	}
	return nil
}

// subscribableResourceType resolves what an entry is about. An entry whose type
// no composition declared followable, or which carries no subject, names no
// resource the host can authorize against — and an entry nobody can be
// authorized for is not one the stream may guess its way into delivering.
func (s *Service) subscribableResourceType(entry JournalEntry) (string, bool) {
	if entry.Subject == "" {
		return "", false
	}
	return s.followableResourceType(entry.Type)
}
