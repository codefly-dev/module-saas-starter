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

// JournalEntry is one committed row of the tenant's journal, in the shape a
// reader needs: the event's identity, what it is about, and the producer's own
// payload carried through unread.
type JournalEntry struct {
	Seq             int64
	EventID         string
	Type            string
	Subject         string
	EventTime       time.Time
	DataContentType string
	Data            []byte
}

// SubscriptionEvent is one journal entry the caller may currently see.
type SubscriptionEvent struct {
	EventID         string
	Type            string
	ResourceType    string
	ResourceID      string
	Time            time.Time
	DataContentType string
	Data            []byte
}

// SubscriptionPageSize bounds one read of the journal. A tenant's publish rate is
// not the reader's to choose, so the stream advances a bounded page at a time and
// comes straight back for the next one rather than materializing a backlog.
const SubscriptionPageSize = 200

// OpenSubscriptionCursor resolves where a stream starts: after the entry the
// client last received, or at the tenant's head when it presents none.
func (s *Service) OpenSubscriptionCursor(ctx context.Context, orgID, lastEventID string) (int64, error) {
	var cursor int64
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		cursor, err = s.store.ResolveTenantJournalCursor(ctx, lastEventID)
		return err
	})
	return cursor, err
}

// ReadSubscriptions returns the entries after cursor that the caller may see,
// and the cursor to read from next.
//
// The returned cursor advances over the whole page, including entries that were
// filtered out. An entry the caller may not see is not pending work: re-reading
// it on the next poll would re-run the same denial forever and stall the stream
// behind it. Filtering leaves a gap in the delivered sequence and nothing else —
// hidden is indistinguishable from an entry that never concerned this reader.
func (s *Service) ReadSubscriptions(ctx context.Context, orgID, userID string, cursor int64) ([]SubscriptionEvent, int64, error) {
	var visible []SubscriptionEvent
	next := cursor
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		page, err := s.store.ListTenantJournal(ctx, cursor, SubscriptionPageSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		next = page[len(page)-1].Seq

		candidates := map[string][]string{}
		for _, entry := range page {
			resourceType, declared := s.subscribableResourceType(entry)
			if !declared {
				continue
			}
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

		for _, entry := range page {
			resourceType, declared := s.subscribableResourceType(entry)
			if !declared {
				continue
			}
			if _, ok := allowed[resourceType][entry.Subject]; !ok {
				continue
			}
			visible = append(visible, SubscriptionEvent{
				EventID:         entry.EventID,
				Type:            entry.Type,
				ResourceType:    resourceType,
				ResourceID:      entry.Subject,
				Time:            entry.EventTime,
				DataContentType: entry.DataContentType,
				Data:            entry.Data,
			})
		}
		return nil
	})
	if err != nil {
		return nil, cursor, err
	}
	return visible, next, nil
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
