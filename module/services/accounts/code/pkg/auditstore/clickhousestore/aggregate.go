package clickhousestore

import (
	"fmt"
	"sort"
	"strings"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"
)

// The aggregation, in ClickHouse. AggregateAuditLog's dimensions and metrics
// run as one GROUP BY over the deduplicated events, ordered as Postgres orders
// them, wherever ClickHouse computes exactly what Postgres does:
//
//   - event_type, actor and category (the event-type index bound as two
//     parallel arrays) are column values;
//   - a time bucket is the UTC day, ISO week or month, printed as Postgres's
//     to_char prints it;
//   - count and count_distinct over a column are exact;
//   - a payload key's numeric reading (a JSON number, or a JSON string holding
//     a plain decimal) parses to the same double — ClickHouse's JSON parser
//     and its precise_float_parsing both round correctly, as float8in does —
//     so min and max are the same, and sum and avg add the same values. The
//     order of the additions is ClickHouse's there and the scan's in
//     Postgres, which no plan fixes: sums of exactly representable values
//     agree to the bit, as two Postgres plans would;
//   - percentile_cont's inputs are collected per group and interpolated in
//     the service (auditeval.PercentileContInPlace), whose rounding matches
//     Postgres's. They are the one thing here that grows with the history, so
//     the statement bounds them before they leave the server (below);
//   - a payload key's ->> text is exact for a string, a boolean, an integer
//     or no value at all.
//
// It is not exact for the ->> text of a fractional number (jsonb keeps its
// scale and digits, ClickHouse reprints the double) or of an object or array
// (jsonb's key order and spacing), nor for any payload ClickHouse's parser
// refuses — valid JSON such as an integer beyond 64 bits, which every JSON
// function then reads as empty. The statement counts the rows where a payload
// dimension or count_distinct meets such a value, and the unparsed payloads
// of any aggregation that reads payloads; when any row counts, the
// aggregation is evaluated in the service over the deduplicated rows instead
// (auditeval), so the answer is Postgres's either way.

// The service holds what a statement returns, so the statement does not return
// more than the aggregation's state budget (auditeval.StateBudget) can count.
// It computes, per group, what the service will charge for the group (a bucket's
// keys and metrics, and for each percentile input scannedSampleBytes) and sums
// it over every group in a window function. When the sum passes the budget, or
// any row is inexact, the statement withholds what grows: the keys come back
// empty and the percentile arrays empty, with the totals beside them, so the
// service refuses (or, for an inexact row, evaluates the aggregation itself)
// from the first row without a driver block ever holding the history. Below the
// budget the rows are returned whole and the service counts them again, with
// the same arithmetic. A percentile's array is also cut to one more than the
// budget could hold (groupArray(max_size)), so the server never builds a group's
// array past what the service could take: a group with more samples than that
// has a sample count (exact, from count) which passes the budget alone, never an
// array that was silently truncated into an inexact percentile.

// scannedSampleBytes is what the service holds for one percentile input while a
// ClickHouse read returns it: the 8 bytes the driver keeps for it in the block it
// decoded, and the 8 of the slice Scan copies out of that block. The percentile
// is taken by sorting that slice in place, which adds no third.
var scannedSampleBytes = 2 * auditeval.SampleBytes(1)

// aggregation is a rendered aggregation statement and how to read its rows:
// per group, the dimensions, the count, each metric, each metric's sample
// count, the count of inexact rows in the whole read, and whether the read's
// state passes the budget.
type aggregation struct {
	statement string
	args      []any
	dims      int
	metrics   []business.AuditMetric
	aliases   []string
}

// numericReading is the regular expression Postgres's numeric reading applies
// to a JSON string (auditNumericExpr); ClickHouse's match is RE2, which reads
// it identically.
const numericReading = `'^-?[0-9]+(\\.[0-9]+)?$'`

// payloadExprs renders the SQL forms of payload keys over the payload column,
// binding each key once.
type payloadExprs struct {
	p        *sqlParams
	keys     map[string]string
	inexact  []string
	inexactK map[string]bool
}

func (x *payloadExprs) key(name string) string {
	if bound, ok := x.keys[name]; ok {
		return bound
	}
	bound := x.p.text(name)
	x.keys[name] = bound
	return bound
}

// text is payload ->> key, NULL when the key is missing or null. A text of a
// kind ClickHouse cannot print as jsonb does is NULL here too, and counted as
// inexact.
func (x *payloadExprs) text(name string) string {
	k := x.key(name)
	if !x.inexactK[name] {
		x.inexactK[name] = true
		x.inexact = append(x.inexact, "JSONType(payload, "+k+") IN ('Double', 'Array', 'Object')")
	}
	return "multiIf(JSONType(payload, " + k + ") = 'String', JSONExtractString(payload, " + k + "), " +
		"JSONType(payload, " + k + ") IN ('Int64', 'UInt64', 'Bool'), JSONExtractRaw(payload, " + k + "), NULL)"
}

// number is the numeric reading of a payload key, NULL when it has none.
func (x *payloadExprs) number(name string) string {
	k := x.key(name)
	return "multiIf(JSONType(payload, " + k + ") IN ('Int64', 'UInt64', 'Double'), JSONExtractFloat(payload, " + k + "), " +
		"JSONType(payload, " + k + ") = 'String' AND match(JSONExtractString(payload, " + k + "), " + numericReading + "), " +
		"toFloat64OrNull(JSONExtractString(payload, " + k + ")), NULL)"
}

// usesPayload reports whether an aggregation reads payloads.
func usesPayload(pl plan, spec business.AuditAggregationSpec) bool {
	if pl.m.NeedsPayload() {
		return true
	}
	for _, dimension := range spec.GroupBy {
		if strings.HasPrefix(dimension, "payload:") {
			return true
		}
	}
	for _, metric := range spec.Metrics {
		if strings.HasPrefix(metric.Field, "payload:") {
			return true
		}
	}
	return false
}

// buildAggregation renders the aggregation of a read whose payload filter, if
// any, is pushed down. ok is false when the read can match nothing.
func (s *Store) buildAggregation(pl plan, spec business.AuditAggregationSpec, types business.AuditEventTypeIndex) (aggregation, bool, error) {
	p := &sqlParams{}
	payload := usesPayload(pl, spec)
	var source string
	if payload {
		joined, ok := s.joinedEvents(p, pl, false)
		if !ok {
			return aggregation{}, false, nil
		}
		source = joined
		if pl.payload != nil {
			source = "SELECT * FROM (" + source + ") WHERE " + payloadCandidates(p, "payload", pl.payload)
		}
	} else {
		events, ok := s.eventsWhere(p, pl)
		if !ok {
			return aggregation{}, false, nil
		}
		source = "SELECT event_type, actor_id, occurred_at, resource, resource_id FROM " + EventsTable +
			" WHERE " + events.String() + " LIMIT 1 BY event_id"
	}

	x := &payloadExprs{p: p, keys: map[string]string{}, inexactK: map[string]bool{}}
	var names, categories string
	category := func() (string, string) {
		if names == "" {
			// Sorted, so the statement is the same for the same index.
			typeNames := make([]string, 0, len(types))
			for name := range types {
				typeNames = append(typeNames, name)
			}
			sort.Strings(typeNames)
			typeCategories := make([]string, len(typeNames))
			for i, name := range typeNames {
				typeCategories[i] = types[name].Category
			}
			names, categories = p.texts(typeNames), p.texts(typeCategories)
		}
		return names, categories
	}

	dims := spec.GroupBy
	if len(dims) == 0 {
		dims = []string{"event_type"}
	}
	var selects, groups, order []string
	for i, dim := range dims {
		var expr string
		if key, ok := strings.CutPrefix(dim, "payload:"); ok && key != "" {
			expr = "ifNull(" + x.text(key) + ", '')"
		} else {
			switch dim {
			case "event_type":
				expr = "toString(event_type)"
			case "actor":
				expr = "actor_id"
			case "category":
				n, c := category()
				expr = "transform(toString(event_type), " + n + ", " + c + ", 'unknown')"
			case "time":
				expr = timeBucket(spec.Bucket)
			default:
				return aggregation{}, false, fmt.Errorf("audit: unhandled group dimension %q", dim)
			}
		}
		alias := fmt.Sprintf("d%d", i)
		selects = append(selects, expr+" AS "+alias)
		groups = append(groups, alias)
		order = append(order, alias+" ASC")
	}
	selects = append(selects, "count() AS cnt")

	aliases := make([]string, len(spec.Metrics))
	samples := make([]string, len(spec.Metrics))
	for i, metric := range spec.Metrics {
		aliases[i] = metric.ResolvedAlias()
		var value, sample string
		switch metric.Op {
		case "", "count":
			value, sample = "toFloat64(count())", "count()"
		case "count_distinct":
			if key, ok := strings.CutPrefix(metric.Field, "payload:"); ok && key != "" {
				text := x.text(key)
				value, sample = "toFloat64(uniqExact("+text+"))", "count("+text+")"
				break
			}
			switch metric.Field {
			case "actor_id", "resource_id":
				// Empty is no value, as NULL is in Postgres.
				value = "toFloat64(uniqExactIf(" + metric.Field + ", " + metric.Field + " != ''))"
				sample = "countIf(" + metric.Field + " != '')"
			case "event_type", "resource":
				value, sample = "toFloat64(uniqExact("+metric.Field+"))", "count()"
			case "category":
				n, c := category()
				known := "if(has(" + n + ", toString(event_type)), transform(toString(event_type), " + n + ", " + c + ", ''), NULL)"
				value, sample = "toFloat64(uniqExact("+known+"))", "count("+known+")"
			default:
				return aggregation{}, false, fmt.Errorf("audit: unhandled count_distinct field %q", metric.Field)
			}
		case "sum", "avg", "min", "max":
			number := x.number(strings.TrimPrefix(metric.Field, "payload:"))
			value, sample = "ifNull("+metric.Op+"("+number+"), 0)", "count("+number+")"
		case "percentile":
			number := x.number(strings.TrimPrefix(metric.Field, "payload:"))
			// One more than the budget can hold: a group that has them all is
			// past the budget, and the sample count says so exactly.
			value, sample = "groupArray("+p.count(s.maxSamples()+1)+")("+number+")", "count("+number+")"
		default:
			return aggregation{}, false, fmt.Errorf("audit: unhandled metric op %q", metric.Op)
		}
		selects = append(selects, fmt.Sprintf("%s AS m%d", value, i))
		samples[i] = fmt.Sprintf("%s AS s%d", sample, i)
	}
	selects = append(selects, samples...)
	inexact := "toUInt64(0)"
	if payload {
		inexact = "countIf(" + strings.Join(append([]string{unparsed("payload")}, x.inexact...), " OR ") + ")"
	}
	selects = append(selects, inexact+" AS inexact")

	orderBy := "cnt DESC, " + strings.Join(order, ", ")
	if len(dims) == 1 && dims[0] == "time" {
		orderBy = "d0 ASC"
	}
	grouped := "SELECT " + strings.Join(selects, ", ") + " FROM (" + source + ") GROUP BY " + strings.Join(groups, ", ")
	return aggregation{statement: s.withheld(p, grouped, len(dims), spec.Metrics, orderBy), args: p.args, dims: len(dims), metrics: spec.Metrics, aliases: aliases}, true, nil
}

// maxSamples is the most percentile inputs a read can return: the budget at
// what each costs the service.
func (s *Store) maxSamples() int {
	return int(s.aggregateMaxBytes / scannedSampleBytes)
}

// withheld wraps the grouped statement (dimensions d0..dn, cnt, metrics m0..,
// sample counts s0.., inexact) in the gate described above, and orders the
// result. Its columns are k0..kn (the dimensions), cnt, v0.. (the metrics),
// s0.., gate_inexact (inexact rows in the whole read) and gate_over (the read's
// state passes the budget). The cost it sums is the arithmetic of
// auditeval.BucketBytes and of scannedSampleBytes.
func (s *Store) withheld(p *sqlParams, grouped string, dims int, metrics []business.AuditMetric, orderBy string) string {
	// BucketBytes of a bucket whose keys are empty, and of one more byte of key.
	fixed := auditeval.BucketBytes(make([]string, dims), len(metrics))
	perKeyByte := auditeval.BucketBytes([]string{"x"}, 0) - auditeval.BucketBytes([]string{""}, 0)
	lengths := make([]string, dims)
	for i := range lengths {
		lengths[i] = fmt.Sprintf("length(d%d)", i)
	}
	cost := p.count(int(fixed)) + " + " + p.count(int(perKeyByte)) + " * (" + strings.Join(lengths, " + ") + ")"
	var percentiles []string
	for i, metric := range metrics {
		if metric.Op == "percentile" {
			percentiles = append(percentiles, fmt.Sprintf("s%d", i))
		}
	}
	if len(percentiles) > 0 {
		cost += " + " + p.count(int(scannedSampleBytes)) + " * (" + strings.Join(percentiles, " + ") + ")"
	}

	const withhold = "(gate_over OR gate_inexact > 0)"
	var selects []string
	for i := 0; i < dims; i++ {
		selects = append(selects, fmt.Sprintf("if(%s, '', d%d) AS k%d", withhold, i, i))
	}
	selects = append(selects, "cnt")
	for i, metric := range metrics {
		if metric.Op == "percentile" {
			selects = append(selects, fmt.Sprintf("if(%s, emptyArrayFloat64(), m%d) AS v%d", withhold, i, i))
		} else {
			selects = append(selects, fmt.Sprintf("m%d AS v%d", i, i))
		}
	}
	for i := range metrics {
		selects = append(selects, fmt.Sprintf("s%d", i))
	}
	selects = append(selects,
		"sum(inexact) OVER () AS gate_inexact",
		"(sum("+cost+") OVER () > "+p.count(int(s.aggregateMaxBytes))+") AS gate_over",
	)
	return "SELECT " + strings.Join(selects, ", ") + " FROM (" + grouped + ") ORDER BY " + orderBy
}

// timeBucket is the time dimension: the UTC start of the event's day (the
// default), ISO week or month, as to_char(date_trunc(grain, created_at),
// 'YYYY-MM-DD"T"HH24:MI:SSOF') prints it under the UTC session time zone.
func timeBucket(bucket string) string {
	start := "toDate(occurred_at, 'UTC')"
	switch bucket {
	case "week":
		start = "toMonday(occurred_at, 'UTC')"
	case "month":
		start = "toStartOfMonth(occurred_at, 'UTC')"
	}
	return "concat(toString(" + start + "), 'T00:00:00+00')"
}
