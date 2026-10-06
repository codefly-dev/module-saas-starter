package auditeval

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unsafe"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// The semantics the Postgres reads give each predicate, dimension and metric,
// as Postgres documents them. The parity test in pkg/business holds the whole
// evaluation to a real Postgres over one fixture; these pin the edges a
// fixture does not reach.

func decoded(t *testing.T, details string) map[string]any {
	t.Helper()
	payload, err := decodeDetails(details)
	require.NoError(t, err)
	return payload
}

func TestJSONBContainment(t *testing.T) {
	payload := decoded(t, `{"a":1,"b":"x","c":[1,"y",{"k":true}],"d":{"e":null,"f":[2,3]},"n":1.50}`)
	for filter, want := range map[string]bool{
		`{}`:                     true,
		`{"a":1}`:                true,
		`{"a":1.0}`:              true, // numbers compare numerically
		`{"n":1.5}`:              true,
		`{"a":"1"}`:              false, // a string never equals a number
		`{"b":"x"}`:              true,
		`{"b":"X"}`:              false,
		`{"c":[1]}`:              true,
		`{"c":["y",1]}`:          true, // order and multiplicity do not matter
		`{"c":[{"k":true}]}`:     true,
		`{"c":[{}]}`:             true,
		`{"c":[2]}`:              false,
		`{"c":1}`:                false, // an array contains a scalar only at the top level
		`{"d":{"e":null}}`:       true,
		`{"d":{"f":[3]}}`:        true,
		`{"d":{}}`:               true,
		`{"d":[]}`:               false, // an object never contains an array
		`{"missing":null}`:       false,
		`{"a":1,"b":"y"}`:        false,
		`{"d":{"e":null,"g":1}}`: false,
	} {
		rhs, err := normalizeJSON(decoded(t, filter))
		require.NoError(t, err)
		require.Equal(t, want, jsonbContains(payload, rhs), filter)
	}
}

func TestJSONBText(t *testing.T) {
	payload := decoded(t, `{"s":"plain \"q\"","n":1.50,"t":true,"z":null,"o":{"bb":[1,"x"],"a":{"c":"\né<"}}}`)
	for key, want := range map[string]string{
		"s": `plain "q"`,
		"n": "1.50",
		"t": "true",
		// jsonb orders keys by length, then bytes, and prints ", " and ": ".
		"o": `{"a": {"c": "\n` + "é" + `<"}, "bb": [1, "x"]}`,
	} {
		got, ok := jsonbText(payload, key)
		require.True(t, ok, key)
		require.Equal(t, want, got, key)
	}
	_, ok := jsonbText(payload, "z")
	require.False(t, ok, "JSON null is SQL NULL")
	_, ok = jsonbText(payload, "missing")
	require.False(t, ok)
	_, ok = jsonbText(nil, "s")
	require.False(t, ok, "no payload reads as NULL")
}

func TestJSONBNumeric(t *testing.T) {
	payload := decoded(t, `{"n":12.5,"i":-3,"s":"7.25","neg":"-1","exp":"1e3","word":"fast","lead":" 1","b":true,"o":{}}`)
	for key, want := range map[string]float64{"n": 12.5, "i": -3, "s": 7.25, "neg": -1} {
		got, ok := jsonbNumeric(payload, key)
		require.True(t, ok, key)
		require.Equal(t, want, got, key)
	}
	for _, key := range []string{"exp", "word", "lead", "b", "o", "missing"} {
		_, ok := jsonbNumeric(payload, key)
		require.False(t, ok, key)
	}
}

func TestTimeBucket(t *testing.T) {
	at := time.Date(2026, 10, 3, 23, 30, 0, 0, time.FixedZone("ahead", 3*3600)) // 20:30 UTC, a Saturday
	require.Equal(t, "2026-10-03T00:00:00+00", TimeBucket(at, ""))
	require.Equal(t, "2026-10-03T00:00:00+00", TimeBucket(at, "day"))
	require.Equal(t, "2026-09-28T00:00:00+00", TimeBucket(at, "week"), "ISO weeks start on Monday")
	require.Equal(t, "2026-10-01T00:00:00+00", TimeBucket(at, "month"))
	sunday := time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC)
	require.Equal(t, "2026-09-28T00:00:00+00", TimeBucket(sunday, "week"))
	monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	require.Equal(t, "2026-10-05T00:00:00+00", TimeBucket(monday, "week"))
}

func TestPercentileCont(t *testing.T) {
	require.Equal(t, 5.0, PercentileCont([]float64{5}, 0.9))
	require.Equal(t, 2.5, PercentileCont([]float64{4, 1, 3, 2}, 0.5))
	require.Equal(t, 4.0, PercentileCont([]float64{4, 1, 3, 2}, 1))
	require.Equal(t, 5.0, PercentileCont([]float64{20, 0, 10}, 0.25))
	require.Equal(t, 0.5+1.75/512, PercentileCont([]float64{100, 0.5, 2.25}, 1.0/1024))
}

func entryAt(id int, at time.Time) business.AuditEntry {
	return business.AuditEntry{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", id), CreatedAt: at}
}

func TestPageKeepsTheNewestOnceEachInAnyOfferOrder(t *testing.T) {
	base := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	var events []*Event
	for i := 1; i <= 6; i++ {
		events = append(events, &Event{Entry: entryAt(i, base.Add(time.Duration(i)*time.Minute))})
	}
	tie := &Event{Entry: entryAt(9, base.Add(3*time.Minute))} // ties event 3; the larger id is newer
	duplicate := &Event{Entry: events[5].Entry}               // event 6 again
	page, err := NewPage(business.AuditQuery{PageSize: 3})
	require.NoError(t, err)
	for _, i := range []int{2, 5, 0, 3, 1, 4} {
		page.Offer(events[i])
	}
	page.Offer(tie)
	page.Offer(duplicate)
	got, next := page.Result()
	require.Equal(t, []string{events[5].Entry.ID, events[4].Entry.ID, events[3].Entry.ID}, []string{got[0].Entry.ID, got[1].Entry.ID, got[2].Entry.ID})
	require.NotEmpty(t, next)

	page, err = NewPage(business.AuditQuery{PageSize: 3, PageToken: next})
	require.NoError(t, err)
	for _, event := range append(events, tie, duplicate) {
		page.Offer(event)
	}
	got, next = page.Result()
	require.Equal(t, []string{tie.Entry.ID, events[2].Entry.ID, events[1].Entry.ID}, []string{got[0].Entry.ID, got[1].Entry.ID, got[2].Entry.ID})
	require.NotEmpty(t, next)

	_, err = NewPage(business.AuditQuery{PageToken: "not a token!"})
	require.ErrorContains(t, err, "invalid page token")
}

func TestDedupe(t *testing.T) {
	seen := NewDedupe()
	require.True(t, seen.First("aaaaaaaa-0000-4000-8000-000000000001"))
	require.False(t, seen.First("aaaaaaaa-0000-4000-8000-000000000001"))
	require.True(t, seen.First("AAAAAAAA-0000-4000-8000-000000000001"), "ids compare as stored, exactly")
	require.True(t, seen.First("not-a-uuid"))
	require.False(t, seen.First("not-a-uuid"))
}

func TestMatcherRefusals(t *testing.T) {
	org := "aaaaaaaa-0000-4000-8000-000000000001"
	_, err := NewMatcher(business.AuditRead{})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
	_, err = NewMatcher(business.AuditRead{Scope: business.OrganizationAuditScope("org-1"), Query: business.AuditQuery{OrgID: "org-1"}})
	require.ErrorContains(t, err, "not a uuid")
	_, err = NewMatcher(business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org, ActorID: "someone"}})
	require.ErrorContains(t, err, "not a uuid")
	_, err = NewMatcher(business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org, Category: "access"}})
	require.ErrorContains(t, err, "event-type index")
	_, err = NewMatcher(business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org, CollectionID: "c"}})
	require.ErrorContains(t, err, "uncompiled collection filter")
	_, err = NewAggregator(business.AuditAggregationSpec{GroupBy: []string{"category"}}, nil, 0)
	require.ErrorContains(t, err, "event-type index")

	m, err := NewMatcher(business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{
		OrgID: org, ActorID: "BBBBBBBB-0000-4000-8000-000000000002",
	}})
	require.NoError(t, err)
	require.Equal(t, "bbbbbbbb-0000-4000-8000-000000000002", m.ActorID(), "an actor id compares as the uuid it names")
}

// Postgres reads an organization id of any spelling of a uuid as the one value
// it names; the stores keep it in the canonical lowercase form, so every read
// carries that form, whatever the caller wrote.
func TestMatcherReadsAnOrganizationInItsCanonicalForm(t *testing.T) {
	canonical := "aaaaaaaa-0000-4000-8000-0000000000ab"
	for _, spelling := range []string{
		strings.ToUpper(canonical),
		"{" + canonical + "}",
		strings.ReplaceAll(canonical, "-", ""),
	} {
		scope := business.OrganizationAuditScope(spelling)
		m, err := NewMatcher(business.AuditRead{Scope: scope, Query: business.AuditQuery{OrgID: spelling}})
		require.NoError(t, err, spelling)
		require.Equal(t, canonical, m.Scope().OrgID(), spelling)
		require.True(t, m.InScope(business.AuditEntry{OrgID: canonical}), spelling)
		require.False(t, m.InScope(business.AuditEntry{OrgID: "aaaaaaaa-0000-4000-8000-0000000000ac"}), spelling)

		canonicalScope, err := CanonicalScope(scope)
		require.NoError(t, err, spelling)
		require.Equal(t, canonical, canonicalScope.OrgID(), spelling)
	}

	platform, err := CanonicalScope(business.PlatformAuditScope())
	require.NoError(t, err)
	require.True(t, platform.Platform())
	_, err = CanonicalScope(business.OrganizationAuditScope("org-1"))
	require.ErrorContains(t, err, "not a uuid")
	_, err = CanonicalScope(business.AuditReadScope{})
	require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
}

// An event decoded out of a shared buffer holds strings that point into it;
// kept as it is, it would keep every row of the buffer alive.
func TestDetachGivesAnEventItsOwnText(t *testing.T) {
	buffer := []byte("0123456789abcdefghijklmnopqrstuvwxyz-details-hash")
	view := func(from, to int) string { return unsafe.String(&buffer[from], to-from) }
	event := &Event{
		Entry: business.AuditEntry{
			ID: view(0, 10), OrgID: view(1, 11), ActorID: view(2, 12), ActorType: view(3, 13), EventType: business.EventType(view(4, 14)),
			Resource: view(5, 15), ResourceID: view(6, 16), IPAddress: view(7, 17), ImpersonatedBy: view(8, 18), ClientID: view(9, 19),
		},
		Details: view(10, 30), DetailsSHA256: view(20, 40),
	}
	before := *event
	event.Detach()
	require.Equal(t, before.Entry, event.Entry, "the same text")
	require.Equal(t, before.Details, event.Details)
	for name, pair := range map[string][2]string{
		"id": {before.Entry.ID, event.Entry.ID}, "org": {before.Entry.OrgID, event.Entry.OrgID}, "actor": {before.Entry.ActorID, event.Entry.ActorID},
		"type": {string(before.Entry.EventType), string(event.Entry.EventType)}, "resource": {before.Entry.Resource, event.Entry.Resource},
		"ip": {before.Entry.IPAddress, event.Entry.IPAddress}, "client": {before.Entry.ClientID, event.Entry.ClientID},
		"details": {before.Details, event.Details}, "hash": {before.DetailsSHA256, event.DetailsSHA256},
	} {
		require.NotEqual(t, uintptr(unsafe.Pointer(unsafe.StringData(pair[0]))), uintptr(unsafe.Pointer(unsafe.StringData(pair[1]))), "%s: its own copy", name)
	}
}

func TestPageReportsWhetherItKeptAnEvent(t *testing.T) {
	base := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	page, err := NewPage(business.AuditQuery{PageSize: 1})
	require.NoError(t, err)
	require.True(t, page.Offer(&Event{Entry: entryAt(2, base.Add(2*time.Minute))}))
	require.True(t, page.Offer(&Event{Entry: entryAt(1, base.Add(time.Minute))}), "the proof of a next page")
	require.False(t, page.Offer(&Event{Entry: entryAt(1, base.Add(time.Minute))}), "an event it holds")
	require.False(t, page.Offer(&Event{Entry: entryAt(0, base)}), "older than everything kept")
	require.True(t, page.Offer(&Event{Entry: entryAt(3, base.Add(3*time.Minute))}), "newer evicts the oldest")
}

func TestEventSizeCountsItsText(t *testing.T) {
	small := &Event{Entry: entryAt(1, time.Time{})}
	large := &Event{Entry: entryAt(1, time.Time{}), Details: strings.Repeat("x", 1000), DetailsSHA256: strings.Repeat("h", 64)}
	require.Equal(t, small.Size()+1064, large.Size())
	require.GreaterOrEqual(t, small.Size(), len(small.Entry.ID))
}
