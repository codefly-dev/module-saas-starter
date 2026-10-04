package bigquerystore

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// restriction is a Storage Read API row restriction: a conjunction of simple
// comparisons the read session evaluates server-side, so BigQuery prunes the
// partitions outside its occurred_at bounds and the blocks outside its org_id
// cluster, and streams back only the rows it admits. It is an optimization and
// a narrowing, never the authority: the service evaluates every predicate
// again on every row it reads (auditeval), the scope included.
type restriction struct {
	terms []string
	err   error
}

func (r *restriction) add(term string) { r.terms = append(r.terms, term) }

// eq adds field = value.
func (r *restriction) eq(field, value string) {
	literal, err := stringLiteral(value)
	if err != nil {
		r.fail(field, err)
		return
	}
	r.add(field + " = " + literal)
}

// in adds field IN (values...). The caller never passes an empty set: a read
// that can match no type reads nothing at all.
func (r *restriction) in(field string, values []string) {
	literals := make([]string, len(values))
	for i, value := range values {
		literal, err := stringLiteral(value)
		if err != nil {
			r.fail(field, err)
			return
		}
		literals[i] = literal
	}
	r.add(field + " IN (" + strings.Join(literals, ", ") + ")")
}

// timeBound adds field op timestamp, for op one of >=, <, <=.
func (r *restriction) timeBound(field, op string, at time.Time) {
	r.add(fmt.Sprintf("%s %s CAST(%q AS TIMESTAMP)", field, op, at.UTC().Format(restrictionTimestampLayout)))
}

func (r *restriction) fail(field string, err error) {
	if r.err == nil {
		r.err = fmt.Errorf("bigquery audit store: cannot restrict %s: %w", field, err)
	}
}

// String is the restriction's text, or "" for none.
func (r *restriction) String() string { return strings.Join(r.terms, " AND ") }

// restrictionTimestampLayout is a GoogleSQL timestamp string at microsecond
// precision, in UTC.
const restrictionTimestampLayout = "2006-01-02 15:04:05.000000+00:00"

// stringLiteral quotes value as a GoogleSQL string literal: printable ASCII
// as itself, a quote and a backslash escaped, and every other code point as a
// \U escape, so no value can end the literal or carry a raw control character.
func stringLiteral(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%q is not valid UTF-8", value)
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, `\U%08X`, r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}
