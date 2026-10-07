package auditeval

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"
	"time"

	"accounts/pkg/business"
)

// DefaultPageSize is the activity list's page size when the query names none,
// as it is in Postgres.
const DefaultPageSize = 50

// Page collects one page of the activity list from events offered in any
// order: the newest PageSize events — by occurrence, then event id, both
// descending — strictly after the query's page token, each once. It keeps one
// more than a page, which is how it knows a further page exists, and never
// more, so a store can offer it every event of a long history in bounded
// memory.
type Page struct {
	size    int
	after   *cursor
	kept    eventHeap
	keptIDs map[string]bool
}

type cursor struct {
	at time.Time
	id string
}

// NewPage starts a page of q: its size (DefaultPageSize when zero) and the
// position its page token names.
func NewPage(q business.AuditQuery) (*Page, error) {
	size := int(q.PageSize)
	if size == 0 {
		size = DefaultPageSize
	}
	if size < 0 {
		return nil, fmt.Errorf("invalid page size %d", q.PageSize)
	}
	p := &Page{size: size, keptIDs: map[string]bool{}}
	if q.PageToken != "" {
		at, id, err := business.DecodeAuditPageToken(q.PageToken)
		if err != nil {
			return nil, fmt.Errorf("invalid page token: %w", err)
		}
		p.after = &cursor{at: at, id: id}
	}
	return p, nil
}

// Size is the page size: the query's, or DefaultPageSize when it names none.
func (p *Page) Size() int { return p.size }

// After is the keyset position the page token names — the last event of the
// previous page — and whether there is one. A store that pages in its own
// engine starts strictly after it.
func (p *Page) After() (time.Time, string, bool) {
	if p.after == nil {
		return time.Time{}, "", false
	}
	return p.after.at, p.after.id, true
}

// Before is the occurrence the page token names, if any: no event at or after
// it beyond the token's own tie-break can be on this page.
func (p *Page) Before() *time.Time {
	if p.after == nil {
		return nil
	}
	at := p.after.at
	return &at
}

// Offer considers an event that matched the read, and reports whether the page
// kept it. A store that decodes events out of a shared buffer detaches (Event.
// Detach) the ones that were kept, and only those.
func (p *Page) Offer(event *Event) bool {
	entry := event.Entry
	if p.after != nil && !newer(business.AuditEntry{CreatedAt: p.after.at, ID: p.after.id}, entry) {
		return false
	}
	if p.keptIDs[entry.ID] {
		return false
	}
	if len(p.kept) == p.size+1 {
		if !newer(entry, p.kept[0].Entry) {
			return false
		}
		evicted := heap.Pop(&p.kept).(*Event)
		delete(p.keptIDs, evicted.Entry.ID)
	}
	// A page keeps the event as its text: the list returns the payload decoded
	// again (Result), so a decode a filter left on it is of no use.
	event.dropDecoded()
	heap.Push(&p.kept, event)
	// The key is its own copy: the event's id may point into a buffer the event
	// is about to be detached from.
	p.keptIDs[strings.Clone(entry.ID)] = true
	return true
}

// Full reports whether the page holds a whole page and the proof of another:
// every event older than all of them can no longer change it.
func (p *Page) Full() bool { return len(p.kept) == p.size+1 }

// Events are the kept events, newest first: the page and, when Full, the one
// past it.
func (p *Page) Events() []*Event {
	out := append([]*Event(nil), p.kept...)
	sort.Slice(out, func(i, j int) bool { return newer(out[i].Entry, out[j].Entry) })
	return out
}

// Result is the page and its next token, empty after the last page.
func (p *Page) Result() ([]*Event, string) {
	events := p.Events()
	if len(events) <= p.size {
		return events, ""
	}
	events = events[:p.size]
	last := events[len(events)-1].Entry
	return events, business.EncodeAuditPageToken(last.CreatedAt, last.ID)
}

// eventHeap is a min-heap by list order: its root is the oldest kept event,
// the first to go when a newer one arrives.
type eventHeap []*Event

func (h eventHeap) Len() int           { return len(h) }
func (h eventHeap) Less(i, j int) bool { return newer(h[j].Entry, h[i].Entry) }
func (h eventHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)        { *h = append(*h, x.(*Event)) }
func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// SortNewestFirst orders events as the activity list does.
func SortNewestFirst(events []*Event) {
	sort.Slice(events, func(i, j int) bool { return newer(events[i].Entry, events[j].Entry) })
}

func sortStrings(values []string) { sort.Strings(values) }
