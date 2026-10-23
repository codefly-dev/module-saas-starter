// Package auditstoragetest is the contract suite of business.AuditStorage. Every
// implementation runs it, so a reader written against one conforming storage
// reads the same answers from another: the activity list page by page, every
// aggregation, the scope every read must name, and the at-least-once delivery a
// storage outside Postgres is allowed.
//
// One fixture is appended through the storage under test, and every answer is
// checked two ways. The suite holds its own oracle of the predicates it asks
// about (organization, actor, type, resource, client, window, flat payload
// values, and category and namespace through the event-type index), so a storage
// is measured against the fixture rather than against another implementation.
// When the harness also supplies a reference — the Postgres storage, over the
// same fixture — every list page and every aggregation must equal the
// reference's answer exactly.
package auditstoragetest

import (
	"context"
	"sort"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Subject is one storage under test, and what the suite needs to feed it.
type Subject struct {
	// Storage is what the suite reads.
	Storage business.AuditStorage
	// Append writes one entry the way a deployment does: Postgres inside the
	// organization's (or, for an entry with no organization, the control plane's)
	// transaction, a warehouse as the delivery task does.
	Append func(ctx context.Context, entry business.AuditEntry) error
	// AtLeastOnce says the storage may hold an entry more than once after a
	// redelivery. The suite then appends two entries a second time, and every
	// answer must be unchanged: a read returns each entry once, by id.
	AtLeastOnce bool
	// NoPercentile says the storage refuses the percentile metric. The suite then
	// requires an aggregation that asks for one to fail with
	// business.ErrAuditPercentileUnsupported, and every other one to succeed.
	NoPercentile bool
	// Reference, when set, is a second storage the answers are compared with,
	// and AppendReference writes the same fixture into it. It is how a
	// warehouse storage is held to the Postgres one. Leave both nil when the
	// storage under test is the Postgres one.
	Reference       business.AuditStorage
	AppendReference func(ctx context.Context, entry business.AuditEntry) error
}

// Harness constructs the subject. New runs once per Run, on an empty store, and
// provisions whatever the storage needs to accept entries written now (for
// Postgres, the monthly partitions).
type Harness struct {
	New func(t *testing.T) *Subject
}

// fixture is the entries the suite appends and the ids that name its parts.
type fixture struct {
	org, otherOrg string
	actor, other  string
	// resource names this run's entries and no one else's. Harness.New asks for
	// an empty store, but the Postgres one is append-only and shared with every
	// other test of its package, so a read across organizations has to be pinned
	// to what this suite wrote rather than to whatever the store already holds.
	resource string
	entries  []business.AuditEntry
	types    business.AuditEventTypeIndex
}

func entry(org, actor string, eventType business.EventType, resourceID string, at time.Time, payload map[string]any) business.AuditEntry {
	return business.AuditEntry{
		ID:            uuid.NewString(),
		OrgID:         org,
		ActorID:       actor,
		ActorType:     business.ActorTypeUser,
		EventType:     eventType,
		SchemaVersion: 1,
		Resource:      "datasource", // replaced by the fixture's own, see newFixture
		ResourceID:    resourceID,
		Payload:       payload,
		CreatedAt:     at,
	}
}

func newFixture() *fixture {
	f := &fixture{
		org: uuid.NewString(), otherOrg: uuid.NewString(),
		actor: uuid.NewString(), other: uuid.NewString(),
		resource: "contract-" + uuid.NewString(),
	}
	// Every instant falls in a partition EnsureAuditPartitions provisions: this
	// month or the one before it.
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	day := 24 * time.Hour
	shared := base.Add(-5 * time.Hour) // two entries share this instant; the id breaks the tie
	readPayload := func(boundary string, duration any, count any, outcome string) map[string]any {
		payload := map[string]any{"boundary": boundary, "duration_ms": duration, "outcome": outcome}
		if count != nil {
			payload["result_count"] = count
		}
		return payload
	}
	f.entries = []business.AuditEntry{
		entry(f.org, f.actor, business.EventAuthLogin, "", base, map[string]any{"method": "password", "client_id": "cli"}),
		entry(f.org, f.actor, business.EventDocumentRead, "doc-1", base.Add(-time.Hour), readPayload("b-1", 12.5, 3, "ok")),
		entry(f.org, f.other, business.EventDocumentRead, "doc-1", base.Add(-2*time.Hour), readPayload("b-1", "7.25", 1, "ok")),
		entry(f.org, f.other, business.EventDocumentRead, "doc-2", shared, readPayload("b-2", "fast", nil, "denied")),
		entry(f.org, f.actor, business.EventDocumentRead, "doc-2", shared, readPayload("b-2", 0.5, 4, "ok")),
		entry(f.org, "", business.EventOrgUpdated, "", base.Add(-26*time.Hour), map[string]any{"name": "Renamed", "slug": "renamed"}),
		entry(f.org, f.actor, business.EventDocumentRead, "doc-3", base.Add(-3*day), map[string]any{
			"boundary": "b-1", "duration_ms": 100, "outcome": "ok", "result_count": 2,
			"tags": []any{"x", map[string]any{"k": 1}}, "nested": map[string]any{"zz": 1, "a": "q"}, "label": "<b>&é",
		}),
		entry(f.org, f.other, business.EventAuthLogin, "", base.Add(-9*day), map[string]any{"method": "passkey"}),
		entry(f.org, f.actor, business.EventDocumentRead, "doc-1", base.Add(-12*day), readPayload("b-3", 2.25, 8, "ok")),
		entry(f.org, f.actor, business.EventOrgUpdated, "", base.Add(-15*day), map[string]any{"name": "First"}),
		entry(f.org, f.other, business.EventDocumentRead, "doc-4", base.Add(-20*day), map[string]any{}),
		// Another organization, and the platform, which only the platform read sees.
		entry(f.otherOrg, f.actor, business.EventDocumentRead, "doc-1", base.Add(-time.Hour), readPayload("b-1", 1, 1, "ok")),
		entry(f.otherOrg, f.actor, business.EventAuthLogin, "", base.Add(-4*day), nil),
		entry("", f.actor, business.EventAuthLogin, "", base.Add(-30*time.Minute), map[string]any{"method": "password"}),
	}
	impersonated := entry(f.org, f.actor, business.EventOrgUpdated, "", base.Add(-6*day), map[string]any{"name": "Acting"})
	impersonated.IsImpersonated, impersonated.ImpersonatedBy, impersonated.ClientID, impersonated.IPAddress = true, f.other, "client-1", "203.0.113.7"
	f.entries = append(f.entries, impersonated)
	for i := range f.entries {
		f.entries[i].Resource = f.resource
	}

	// The facts a storage outside Postgres takes from the registry's projection.
	f.types = business.AuditEventTypeIndex{}
	for _, definition := range business.AuditEventCatalog() {
		f.types[string(definition.Type)] = business.AuditEventTypeFacts{
			Category: string(definition.Category), Namespace: definition.Namespace,
		}
	}
	return f
}

// Run runs the contract against the harness's storage.
func Run(t *testing.T, h Harness) {
	ctx := context.Background()
	subject := h.New(t)
	require.NotNil(t, subject)
	require.NotNil(t, subject.Storage)
	require.NotNil(t, subject.Append)
	require.Equal(t, subject.Reference == nil, subject.AppendReference == nil, "a reference needs both its storage and its writer")

	f := newFixture()
	for _, e := range f.entries {
		require.NoError(t, subject.Append(ctx, e))
		if subject.AppendReference != nil {
			require.NoError(t, subject.AppendReference(ctx, e))
		}
	}
	if subject.AtLeastOnce {
		// A delivery that was retried after its write succeeded.
		require.NoError(t, subject.Append(ctx, f.entries[0]))
		require.NoError(t, subject.Append(ctx, f.entries[5]))
	}

	week := time.Now().Add(-7 * 24 * time.Hour)
	recent := time.Now().Add(-3 * time.Hour)
	queries := map[string]business.AuditQuery{
		"organization":       {OrgID: f.org},
		"actor":              {OrgID: f.org, ActorID: f.actor},
		"event type":         {OrgID: f.org, EventType: string(business.EventDocumentRead)},
		"event type set":     {OrgID: f.org, EventTypes: []string{string(business.EventAuthLogin), string(business.EventOrgUpdated)}},
		"type and set":       {OrgID: f.org, EventType: string(business.EventAuthLogin), EventTypes: []string{string(business.EventOrgUpdated)}},
		"category":           {OrgID: f.org, Category: "organization"},
		"namespace":          {OrgID: f.org, Namespace: "saas"},
		"resource":           {OrgID: f.org, Resource: f.resource, ResourceID: "doc-1"},
		"client":             {OrgID: f.org, ClientID: "client-1"},
		"payload":            {OrgID: f.org, PayloadContains: map[string]any{"boundary": "b-1"}},
		"payload number":     {OrgID: f.org, PayloadContains: map[string]any{"result_count": 3.0}},
		"payload nested":     {OrgID: f.org, PayloadContains: map[string]any{"nested": map[string]any{"a": "q"}, "tags": []any{"x"}}},
		"payload pair":       {OrgID: f.org, PayloadContains: map[string]any{"boundary": "b-2", "outcome": "denied"}},
		"payload absent":     {OrgID: f.org, PayloadContains: map[string]any{"outcome": nil}},
		"window":             {OrgID: f.org, From: &week, To: &recent},
		"platform for actor": {ActorID: f.actor},
		// Every organization and the entries with none, which only the platform
		// scope reads: pinned to this run's resource, see fixture.resource.
		"platform":           {Resource: f.resource},
		"other organization": {OrgID: f.otherOrg},
	}

	read := func(q business.AuditQuery) business.AuditRead {
		return business.AuditRead{Scope: scopeOf(q), Query: q, Types: f.types}
	}

	t.Run("list", func(t *testing.T) {
		all, _, err := subject.Storage.List(ctx, read(business.AuditQuery{OrgID: f.org, PageSize: 100}))
		require.NoError(t, err)
		require.Len(t, all, 12, "the fixture is what the comparisons compare: not two empty answers")

		for name, q := range queries {
			want, oracle := f.expected(q)
			for _, size := range []int32{0, 1, 3} {
				q := q
				q.PageSize = size
				var got []business.AuditEntry
				var reference []business.AuditEntry
				var token, referenceToken string
				for pages := 0; ; pages++ {
					require.Less(t, pages, 200, "%s, page size %d: the walk does not end", name, size)
					q.PageToken = token
					page, next, err := subject.Storage.List(ctx, read(q))
					require.NoError(t, err, name)
					limit := int(size)
					if limit == 0 {
						limit = 50
					}
					require.LessOrEqual(t, len(page), limit, "%s, page size %d", name, size)
					got = append(got, page...)
					if subject.Reference != nil {
						q.PageToken = referenceToken
						refPage, refNext, err := subject.Reference.List(ctx, read(q))
						require.NoError(t, err, name)
						require.Equal(t, refPage, page, "%s, page size %d, page %d", name, size, pages)
						require.Equal(t, refNext, next, "%s, page size %d, page %d: next token", name, size, pages)
						reference, referenceToken = append(reference, refPage...), refNext
					}
					if next == "" {
						break
					}
					token = next
				}
				if oracle {
					require.Equal(t, ids(want), ids(got), "%s, page size %d", name, size)
				}
				ordered := sort.SliceIsSorted(got, func(i, j int) bool {
					if !got[i].CreatedAt.Equal(got[j].CreatedAt) {
						return got[i].CreatedAt.After(got[j].CreatedAt)
					}
					return got[i].ID > got[j].ID
				})
				require.True(t, ordered, "%s, page size %d: newest first, ties by id descending", name, size)
				require.Equal(t, len(ids(got)), len(unique(ids(got))), "%s, page size %d: an entry once", name, size)
			}
		}
	})

	t.Run("round trip", func(t *testing.T) {
		got, _, err := subject.Storage.List(ctx, read(business.AuditQuery{OrgID: f.org, PageSize: 100}))
		require.NoError(t, err)
		byID := map[string]business.AuditEntry{}
		for _, e := range got {
			byID[e.ID] = e
		}
		for _, e := range f.entries {
			if e.OrgID != f.org {
				continue
			}
			stored, ok := byID[e.ID]
			require.True(t, ok, "entry %s is listed", e.ID)
			require.Equal(t, e.EventType, stored.EventType)
			require.Equal(t, e.ActorType, stored.ActorType)
			require.Equal(t, e.Resource, stored.Resource)
			require.Equal(t, e.ResourceID, stored.ResourceID)
			require.Equal(t, e.ActorID, stored.ActorID)
			require.Equal(t, e.IsImpersonated, stored.IsImpersonated)
			require.Equal(t, e.ImpersonatedBy, stored.ImpersonatedBy)
			require.Equal(t, e.ClientID, stored.ClientID)
			require.True(t, e.CreatedAt.Equal(stored.CreatedAt), "occurrence %s != %s", e.CreatedAt, stored.CreatedAt)
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		for queryName, q := range queries {
			for specName, spec := range aggregationSpecs() {
				got, err := subject.Storage.Aggregate(ctx, read(q), spec)
				if subject.NoPercentile && spec.HasPercentile() {
					require.ErrorIs(t, err, business.ErrAuditPercentileUnsupported, "%s / %s", queryName, specName)
					continue
				}
				require.NoError(t, err, "%s / %s", queryName, specName)
				if queryName == "organization" {
					require.NotEmpty(t, got, specName)
				}
				if subject.Reference != nil {
					want, err := subject.Reference.Aggregate(ctx, read(q), spec)
					require.NoError(t, err, "%s / %s", queryName, specName)
					require.Equal(t, want, got, "%s / %s", queryName, specName)
				}
			}
			// The default aggregation counts per event type: its buckets add up
			// to exactly the entries the same query lists.
			if expected, oracle := f.expected(q); oracle {
				got, err := subject.Storage.Aggregate(ctx, read(q), business.AuditAggregationSpec{})
				require.NoError(t, err, queryName)
				var total int64
				for _, bucket := range got {
					total += bucket.Count
				}
				require.EqualValues(t, len(expected), total, "%s: the default aggregation counts the entries listed", queryName)
			}
		}
	})

	t.Run("scope", func(t *testing.T) {
		_, _, err := subject.Storage.List(ctx, business.AuditRead{Query: business.AuditQuery{OrgID: f.org}})
		require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
		_, err = subject.Storage.Aggregate(ctx, business.AuditRead{}, business.AuditAggregationSpec{})
		require.ErrorIs(t, err, business.ErrAuditReadUnscoped)
		_, _, err = subject.Storage.List(ctx, business.AuditRead{
			Scope: business.OrganizationAuditScope(f.org), Query: business.AuditQuery{OrgID: f.otherOrg},
		})
		require.Error(t, err, "a query naming another organization than its scope is refused")
		_, _, err = subject.Storage.List(ctx, business.AuditRead{
			Scope: business.PlatformAuditScope(), Query: business.AuditQuery{OrgID: f.org},
		})
		require.Error(t, err, "the platform scope's query names no organization")
		_, err = subject.Storage.Aggregate(ctx, business.AuditRead{
			Scope: business.OrganizationAuditScope(f.org), Query: business.AuditQuery{OrgID: f.org, CollectionID: "uncompiled"},
		}, business.AuditAggregationSpec{})
		require.Error(t, err, "a collection filter reaches a storage only compiled into a payload predicate")
	})
}

// scopeOf is the scope a read of q is made in: its organization, or the
// platform's when it names none.
func scopeOf(q business.AuditQuery) business.AuditReadScope {
	if q.OrgID == "" {
		return business.PlatformAuditScope()
	}
	return business.OrganizationAuditScope(q.OrgID)
}

func aggregationSpecs() map[string]business.AuditAggregationSpec {
	return map[string]business.AuditAggregationSpec{
		"default":  {},
		"category": {GroupBy: []string{"category"}},
		"actor":    {GroupBy: []string{"actor"}, Metrics: []business.AuditMetric{{Op: "count"}, {Op: "count_distinct", Field: "resource_id"}}},
		"day":      {GroupBy: []string{"time"}, Bucket: "day"},
		"week":     {GroupBy: []string{"time"}, Bucket: "week", Metrics: []business.AuditMetric{{Op: "count_distinct", Field: "actor_id"}}},
		"month":    {GroupBy: []string{"time"}, Bucket: "month"},
		"payload dimensions": {GroupBy: []string{"event_type", "payload:outcome"}, Metrics: []business.AuditMetric{
			{Op: "count_distinct", Field: "payload:boundary"},
			{Op: "count_distinct", Field: "category"},
			{Op: "count_distinct", Field: "event_type"},
			{Op: "count_distinct", Field: "resource"},
		}},
		"nested payload dimension": {GroupBy: []string{"payload:tags", "payload:nested", "payload:label"}},
		"numeric payload dimension": {GroupBy: []string{"payload:result_count"}, Metrics: []business.AuditMetric{
			{Op: "count_distinct", Field: "payload:result_count"},
		}},
		"fractional payload dimension": {GroupBy: []string{"payload:duration_ms"}},
		"metrics by type": {GroupBy: []string{"event_type"}, Metrics: []business.AuditMetric{
			{Op: "count", Alias: "events"},
			{Op: "sum", Field: "payload:duration_ms"},
			{Op: "avg", Field: "payload:result_count"},
			{Op: "min", Field: "payload:duration_ms"},
			{Op: "max", Field: "payload:result_count"},
			{Op: "percentile", Field: "payload:duration_ms", Percentile: 0.25, Alias: "p25"},
			{Op: "count_distinct", Field: "payload:outcome"},
			{Op: "count_distinct", Field: "category"},
			{Op: "count_distinct", Field: "actor_id"},
		}, Derived: []business.AuditDerivedMetric{
			{Alias: "duration_per_event", Numerator: "sum_duration_ms", Denominator: "events"},
		}},
		"category by month": {GroupBy: []string{"category", "time"}, Bucket: "month", Metrics: []business.AuditMetric{
			{Op: "count_distinct", Field: "resource_id"},
		}},
		"metrics": {GroupBy: []string{"payload:boundary"}, Metrics: []business.AuditMetric{
			{Op: "count", Alias: "reads"},
			{Op: "sum", Field: "payload:duration_ms"},
			{Op: "avg", Field: "payload:duration_ms"},
			{Op: "min", Field: "payload:duration_ms"},
			{Op: "max", Field: "payload:duration_ms"},
			{Op: "percentile", Field: "payload:duration_ms", Percentile: 0.5, Alias: "p50"},
			{Op: "percentile", Field: "payload:duration_ms", Percentile: 0.9, Alias: "p90"},
			{Op: "sum", Field: "payload:result_count", Alias: "results"},
			{Op: "max", Field: "payload:missing", Alias: "absent"},
		}, Derived: []business.AuditDerivedMetric{
			{Alias: "results_per_read", Numerator: "results", Denominator: "reads"},
			{Alias: "absent_ratio", Numerator: "absent", Denominator: "reads"},
		}},
	}
}

// expected is the fixture's entries a read of q returns, newest first, and
// whether the suite's oracle can answer q at all: a payload predicate on a
// nested value or a null is left to the reference.
func (f *fixture) expected(q business.AuditQuery) ([]business.AuditEntry, bool) {
	var out []business.AuditEntry
	for _, e := range f.entries {
		match, ok := f.matches(q, e)
		if !ok {
			return nil, false
		}
		if match {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, true
}

func (f *fixture) matches(q business.AuditQuery, e business.AuditEntry) (match, ok bool) {
	if q.OrgID != "" && e.OrgID != q.OrgID {
		return false, true
	}
	if q.ActorID != "" && e.ActorID != q.ActorID {
		return false, true
	}
	if q.EventType != "" && string(e.EventType) != q.EventType {
		return false, true
	}
	if len(q.EventTypes) > 0 {
		in := false
		for _, eventType := range q.EventTypes {
			in = in || string(e.EventType) == eventType
		}
		if !in {
			return false, true
		}
	}
	facts := f.types[string(e.EventType)]
	if q.Category != "" && facts.Category != q.Category {
		return false, true
	}
	if q.Namespace != "" && facts.Namespace != q.Namespace {
		return false, true
	}
	if q.Resource != "" && e.Resource != q.Resource {
		return false, true
	}
	if q.ResourceID != "" && e.ResourceID != q.ResourceID {
		return false, true
	}
	if q.ClientID != "" && e.ClientID != q.ClientID {
		return false, true
	}
	if q.From != nil && e.CreatedAt.Before(*q.From) {
		return false, true
	}
	if q.To != nil && e.CreatedAt.After(*q.To) {
		return false, true
	}
	for key, want := range q.PayloadContains {
		wantNumber, isNumber := number(want)
		switch want.(type) {
		case string, bool:
		default:
			if !isNumber {
				return false, false // nested, a list or null: the reference answers it
			}
		}
		have, present := e.Payload[key]
		if !present {
			return false, true
		}
		if isNumber {
			haveNumber, ok := number(have)
			if !ok || haveNumber != wantNumber {
				return false, true
			}
			continue
		}
		if have != want {
			return false, true
		}
	}
	return true, true
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

func ids(entries []business.AuditEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

func unique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
