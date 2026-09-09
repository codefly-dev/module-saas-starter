package eventcatalog

import "strings"

// Namespace returns the leading dotted segment of an event type — the namespace
// a producer must own to publish it. "reference.console.viewed" → "reference".
// A type with no dot is its own namespace; an empty type yields "".
func Namespace(eventType string) string {
	namespace, _, _ := strings.Cut(eventType, ".")
	return namespace
}

// publishedIndex resolves an event type to its declared published contract. It
// is built once over the compose-generated table; the table is small (one entry
// per published event across the composed solutions) so an exact-match map is
// sufficient — pattern matching against it is the caller's job.
var publishedIndex = func() map[string]PublishedEvent {
	m := make(map[string]PublishedEvent, len(published))
	for _, e := range published {
		m[e.Type] = e
	}
	return m
}()

// LookupPublished returns the declared contract for a published event type and
// whether the type is declared in the composed catalog at all.
func LookupPublished(eventType string) (PublishedEvent, bool) {
	e, ok := publishedIndex[eventType]
	return e, ok
}

// IsInternalPublished reports whether the event type is a published type declared
// with internal visibility — an intra-platform event that must never be delivered
// to a subscriber principal. A type absent from the catalog is not internal (only
// an explicit internal declaration suppresses delivery). The relay consults this
// at fan-out time so an internal event is refused delivery even to a subscription
// that predates the type's registration, which the subscribe-time gate over
// InternalPublishedTypes cannot retract.
func IsInternalPublished(eventType string) bool {
	e, ok := publishedIndex[eventType]
	return ok && e.Visibility == "internal"
}

// InternalPublishedTypes returns the types of every published event declared
// with internal visibility. The Subscribe authority gate rejects a solution
// principal whose type pattern would match any of these, so an internal event
// is never delivered to a tenant-scoped subscriber. The list is small; callers
// match their pattern against it directly.
func InternalPublishedTypes() []string {
	var out []string
	for _, e := range published {
		if e.Visibility == "internal" {
			out = append(out, e.Type)
		}
	}
	return out
}
