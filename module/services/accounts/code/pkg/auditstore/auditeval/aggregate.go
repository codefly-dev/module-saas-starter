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
type Aggregator struct {
	spec    business.AuditAggregationSpec
	dims    []string
	types   business.AuditEventTypeIndex
	aliases []string
	groups  map[string]*group
	order   []*group
}

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
func NewAggregator(spec business.AuditAggregationSpec, types business.AuditEventTypeIndex) (*Aggregator, error) {
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
			acc.distinct[value] = struct{}{}
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
				bucket.Metrics[alias] = percentileCont(acc.values, metric.Percentile)
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

// percentileCont is Postgres's percentile_cont: the value at fraction p of the
// sorted inputs, interpolated linearly between the two nearest.
//
// The interpolation is written as Postgres's float8_lerp writes it, lo + pct
// * (hi - lo), and left for the compiler to treat as C compilers treat it: Go
// and GCC both fuse that multiply-add into one rounding on arm64 and neither
// does on baseline amd64, so the result matches the Postgres of the same
// architecture to the last bit. Forcing either rounding would match only one.
func percentileCont(values []float64, p float64) float64 {
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
