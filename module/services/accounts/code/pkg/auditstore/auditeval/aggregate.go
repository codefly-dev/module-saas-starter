package auditeval

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"accounts/pkg/business"
)

// Aggregator computes AggregateAuditLog over the events offered to it, with
// the Postgres aggregation's semantics:
//
//   - dimensions: event_type; category from the event-type index ("unknown"
//     for a type it does not name); actor (the actor id, or ""); time, the UTC
//     start of the event's day, ISO week or month, printed as Postgres prints
//     it under the UTC session time zone ("2026-10-05T00:00:00+00"); and
//     payload:<key>, the key's ->> text or "";
//   - metrics: count; count_distinct over a column or payload key's text,
//     ignoring NULLs; sum, avg, min, max and percentile (percentile_cont) over
//     a payload key read as a number, ignoring what is not one;
//   - every metric's sample count is the number of non-NULL inputs, and a
//     metric with none is absent rather than 0; a derived ratio is absent when
//     an operand is absent or the denominator is 0;
//   - order: a sole time dimension ascending; otherwise by count descending,
//     then by the dimensions ascending, compared bytewise.
//
// An event without details (a content-class event past the content window)
// has no payload: its payload dimensions are "" and its payload metrics NULL.
//
// What an Aggregator keeps grows with the events it is offered — a bucket per
// distinct key, a float per percentile input, a string per distinct value —
// and a store offers it every event of a history, window by window, so it
// counts that state against a budget (StateBudget) and gives up with
// business.ErrAuditAggregateTooLarge when it passes. An Aggregator that has
// returned an error from Add has an incomplete state and must be discarded.
type Aggregator struct {
	spec    business.AuditAggregationSpec
	dims    []string
	types   business.AuditEventTypeIndex
	aliases []string
	groups  map[string]*group
	order   []*group
	state   *StateBudget
}

// What the state of an aggregation costs, counted rather than measured so the
// bound is one number the deployment chose. The figures err high: a bucket's
// cost includes the bucket it is answered as, and a sample's the 8 bytes of its
// float, not the capacity a slice that grows by doubling may hold past it.
const (
	// bucketBytes is a bucket's own: the group, its entries in the map and the
	// ordered list, and the answer built from it.
	bucketBytes = 256
	// keyBytes is what each of a bucket's dimension values costs beside its
	// text, which is held twice (as the key and in the bucket's identity).
	keyBytes = 32
	// metricBytes is a bucket's accumulator for one metric and its entries in
	// the answer's Samples and Metrics maps.
	metricBytes = 160
	// sampleBytes is a percentile input: one float64.
	sampleBytes = 8
	// distinctEntryBytes is what a value a distinct count remembers costs beside
	// its text: its string header and its entry in the set.
	distinctEntryBytes = 32
)

// BucketBytes is the state of one bucket of an aggregation with metrics
// metrics, whose dimension values are keys.
func BucketBytes(keys []string, metrics int) int64 {
	n := int64(bucketBytes + len(keys)*keyBytes + metrics*metricBytes)
	for _, key := range keys {
		n += 2 * int64(len(key))
	}
	return n
}

// SampleBytes is the state of n percentile inputs.
func SampleBytes(n int) int64 { return int64(n) * sampleBytes }

// StateBudget counts the bytes an aggregation retains across every window it
// reads. The window budget of a store bounds what one window holds; this is
// the bound on what outlives a window.
type StateBudget struct {
	limit, held int64
}

// NewStateBudget is a budget of limit bytes; zero is
// business.AuditAggregateMaxBytes.
func NewStateBudget(limit int64) (*StateBudget, error) {
	switch {
	case limit < 0:
		return nil, errors.New("audit: the aggregation state budget cannot be negative")
	case limit == 0:
		limit = business.AuditAggregateMaxBytes
	}
	return &StateBudget{limit: limit}, nil
}

// Take counts n more bytes as retained, and reports
// business.ErrAuditAggregateTooLarge once they pass the limit. The bytes are
// counted either way: a caller that gets the error stops.
func (b *StateBudget) Take(n int64) error {
	b.held += n
	if b.held > b.limit {
		return fmt.Errorf("%w (the bound is %d bytes)", business.ErrAuditAggregateTooLarge, b.limit)
	}
	return nil
}

// Held is the bytes counted so far.
func (b *StateBudget) Held() int64 { return b.held }

type group struct {
	keys    []string
	count   int64
	metrics []*accumulator
}

type accumulator struct {
	samples  int64
	sum      float64
	min, max float64
	values   []float64
	distinct map[string]struct{}
}

// NewAggregator prepares spec, which the caller has validated. It refuses a
// spec that groups or counts by category without the event-type index, and
// any dimension or metric the Postgres aggregation would not run either.
// maxStateBytes is the bound on the state it keeps (StateBudget); zero is
// business.AuditAggregateMaxBytes.
func NewAggregator(spec business.AuditAggregationSpec, types business.AuditEventTypeIndex, maxStateBytes int64) (*Aggregator, error) {
	state, err := NewStateBudget(maxStateBytes)
	if err != nil {
		return nil, err
	}
	dims := spec.GroupBy
	if len(dims) == 0 {
		dims = []string{"event_type"}
	}
	needsTypes := false
	for _, dim := range dims {
		if _, ok := payloadKey(dim); ok {
			continue
		}
		switch dim {
		case "event_type", "actor", "time":
		case "category":
			needsTypes = true
		default:
			return nil, fmt.Errorf("audit: unhandled group dimension %q", dim)
		}
	}
	aliases := make([]string, len(spec.Metrics))
	for i, metric := range spec.Metrics {
		switch metric.Op {
		case "", "count":
		case "count_distinct":
			if _, ok := payloadKey(metric.Field); !ok {
				switch metric.Field {
				case "actor_id", "resource_id", "event_type", "resource":
				case "category":
					needsTypes = true
				default:
					return nil, fmt.Errorf("audit: unhandled count_distinct field %q", metric.Field)
				}
			}
		case "sum", "avg", "min", "max", "percentile":
		default:
			return nil, fmt.Errorf("audit: unhandled metric op %q", metric.Op)
		}
		aliases[i] = metric.ResolvedAlias()
	}
	if needsTypes && types == nil {
		return nil, errors.New("audit: a category dimension or count needs the event-type index")
	}
	return &Aggregator{
		spec:    spec,
		dims:    dims,
		types:   types,
		aliases: aliases,
		groups:  map[string]*group{},
		state:   state,
	}, nil
}

func payloadKey(field string) (string, bool) {
	key, ok := strings.CutPrefix(field, "payload:")
	return key, ok && key != ""
}

// Add counts one matching event. The caller offers each event once.
func (a *Aggregator) Add(event *Event) error {
	payload, err := event.payload()
	if err != nil {
		return fmt.Errorf("audit read: details of event %s: %w", event.Entry.ID, err)
	}
	keys := make([]string, len(a.dims))
	for i, dim := range a.dims {
		keys[i] = a.dimension(dim, event.Entry, payload)
	}
	id := groupID(keys)
	g, ok := a.groups[id]
	if !ok {
		if err := a.state.Take(BucketBytes(keys, len(a.spec.Metrics))); err != nil {
			return err
		}
		g = &group{keys: keys, metrics: make([]*accumulator, len(a.spec.Metrics))}
		for i := range g.metrics {
			g.metrics[i] = &accumulator{min: math.Inf(1), max: math.Inf(-1)}
		}
		a.groups[id] = g
		a.order = append(a.order, g)
	}
	g.count++
	for i, metric := range a.spec.Metrics {
		acc := g.metrics[i]
		switch metric.Op {
		case "", "count":
			acc.samples++
		case "count_distinct":
			value, ok := a.distinctValue(metric.Field, event.Entry, payload)
			if !ok {
				continue
			}
			acc.samples++
			if acc.distinct == nil {
				acc.distinct = map[string]struct{}{}
			}
			if _, seen := acc.distinct[value]; !seen {
				if err := a.state.Take(distinctEntryBytes + int64(len(value))); err != nil {
					return err
				}
				acc.distinct[value] = struct{}{}
			}
		default:
			key, _ := payloadKey(metric.Field)
			value, ok := jsonbNumeric(payload, key)
			if !ok {
				continue
			}
			acc.samples++
			acc.sum += value
			acc.min = math.Min(acc.min, value)
			acc.max = math.Max(acc.max, value)
			if metric.Op == "percentile" {
				if err := a.state.Take(sampleBytes); err != nil {
					return err
				}
				acc.values = append(acc.values, value)
			}
		}
	}
	return nil
}

func groupID(keys []string) string {
	var id strings.Builder
	for _, key := range keys {
		id.WriteString(fmt.Sprintf("%d:", len(key)))
		id.WriteString(key)
	}
	return id.String()
}

func (a *Aggregator) dimension(dim string, entry business.AuditEntry, payload map[string]any) string {
	if key, ok := payloadKey(dim); ok {
		text, _ := jsonbText(payload, key)
		return text
	}
	switch dim {
	case "category":
		if facts, ok := a.types[string(entry.EventType)]; ok {
			return facts.Category
		}
		return "unknown"
	case "actor":
		return entry.ActorID
	case "time":
		return TimeBucket(entry.CreatedAt, a.spec.Bucket)
	default:
		return string(entry.EventType)
	}
}

// TimeBucket is the time dimension's key: the UTC start of the event's day
// (the default), ISO week or month, as Postgres's
// to_char(date_trunc(grain, created_at), 'YYYY-MM-DD"T"HH24:MI:SSOF') prints it
// under the UTC session time zone.
func TimeBucket(at time.Time, bucket string) string {
	at = at.UTC()
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	switch bucket {
	case "week":
		day = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	case "month":
		day = time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return day.Format("2006-01-02T15:04:05") + "+00"
}

func (a *Aggregator) distinctValue(field string, entry business.AuditEntry, payload map[string]any) (string, bool) {
	if key, ok := payloadKey(field); ok {
		return jsonbText(payload, key)
	}
	switch field {
	case "actor_id":
		return entry.ActorID, entry.ActorID != ""
	case "resource_id":
		return entry.ResourceID, entry.ResourceID != ""
	case "event_type":
		return string(entry.EventType), true
	case "resource":
		return entry.Resource, true
	case "category":
		facts, ok := a.types[string(entry.EventType)]
		return facts.Category, ok
	default:
		return "", false
	}
}

// Buckets is the aggregation result, ordered as Postgres orders it; nil when no
// event matched.
func (a *Aggregator) Buckets() []business.AuditAggregateBucket {
	groups := append([]*group(nil), a.order...)
	soleTime := len(a.dims) == 1 && a.dims[0] == "time"
	sort.SliceStable(groups, func(i, j int) bool {
		gi, gj := groups[i], groups[j]
		if !soleTime && gi.count != gj.count {
			return gi.count > gj.count
		}
		for k := range gi.keys {
			if gi.keys[k] != gj.keys[k] {
				return gi.keys[k] < gj.keys[k]
			}
		}
		return false
	})
	var out []business.AuditAggregateBucket
	for _, g := range groups {
		bucket := business.AuditAggregateBucket{
			Keys:    append([]string(nil), g.keys...),
			Count:   g.count,
			Samples: make(map[string]int64, len(a.aliases)),
			Metrics: make(map[string]float64, len(a.aliases)+len(a.spec.Derived)),
		}
		bucket.Key = bucket.Keys[0]
		for i, metric := range a.spec.Metrics {
			alias := a.aliases[i]
			acc := g.metrics[i]
			bucket.Samples[alias] = acc.samples
			if acc.samples == 0 {
				continue
			}
			switch metric.Op {
			case "", "count":
				bucket.Metrics[alias] = float64(g.count)
			case "count_distinct":
				bucket.Metrics[alias] = float64(len(acc.distinct))
			case "sum":
				bucket.Metrics[alias] = acc.sum
			case "avg":
				bucket.Metrics[alias] = acc.sum / float64(acc.samples)
			case "min":
				bucket.Metrics[alias] = acc.min
			case "max":
				bucket.Metrics[alias] = acc.max
			case "percentile":
				bucket.Metrics[alias] = PercentileCont(acc.values, metric.Percentile)
			}
		}
		for _, derived := range a.spec.Derived {
			numerator, numeratorOK := bucket.Metrics[derived.Numerator]
			denominator, denominatorOK := bucket.Metrics[derived.Denominator]
			if numeratorOK && denominatorOK && denominator != 0 {
				bucket.Metrics[derived.Alias] = numerator / denominator
			}
		}
		out = append(out, bucket)
	}
	return out
}

// PercentileCont is Postgres's percentile_cont: the value at fraction p of the
// sorted inputs, interpolated linearly between the two nearest. values need
// not be sorted and is not modified; it must not be empty. A store that
// aggregates in its own engine collects a group's values there and computes
// the percentile here, so the interpolation rounds as Postgres's does.
//
// The interpolation is written as Postgres's float8_lerp writes it, lo + pct
// * (hi - lo), and left for the compiler to treat as C compilers treat it: Go
// and GCC both fuse that multiply-add into one rounding on arm64 and neither
// does on baseline amd64, so the result matches the Postgres of the same
// architecture to the last bit. Forcing either rounding would match only one.
func PercentileCont(values []float64, p float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	position := p * float64(len(sorted)-1)
	first := math.Floor(position)
	second := math.Ceil(position)
	low, high := sorted[int(first)], sorted[int(second)]
	if first == second {
		return low
	}
	return low + (position-first)*(high-low)
}
