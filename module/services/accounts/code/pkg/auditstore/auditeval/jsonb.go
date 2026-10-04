package auditeval

import (
	"bytes"
	"encoding/json"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The payload semantics of the Postgres reads, reproduced over a decoded
// payload: jsonb containment (@>), the text of a key (->>), and the numeric
// reading of a key that AggregateAuditLog's metrics use. Payloads are decoded
// with json.Number (decodeDetails), so a number keeps the text Postgres
// printed for it, and a store's canonical details — which were serialized
// from that text — read exactly as the jsonb they came from.

// decodeDetails decodes a canonical details string keeping each number's text.
func decodeDetails(details string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(details))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// normalizeJSON is value as Postgres receives it when it binds the value as
// jsonb: marshalled, then read back with json.Number.
func normalizeJSON(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// jsonbContains is lhs @> rhs for two jsonb values: every key of an object on
// the right is in the object on the left with a value that contains it; every
// element of an array on the right is contained by some element of the array
// on the left; scalars are equal (numbers numerically). Containers of
// different kinds never contain one another.
func jsonbContains(lhs, rhs any) bool {
	switch right := rhs.(type) {
	case map[string]any:
		left, ok := lhs.(map[string]any)
		if !ok {
			return false
		}
		for key, rightValue := range right {
			leftValue, ok := left[key]
			if !ok {
				return false
			}
			if isContainer(rightValue) {
				if !jsonbContains(leftValue, rightValue) {
					return false
				}
				continue
			}
			if !jsonbScalarEqual(leftValue, rightValue) {
				return false
			}
		}
		return true
	case []any:
		left, ok := lhs.([]any)
		if !ok {
			return false
		}
		for _, rightElement := range right {
			found := false
			for _, leftElement := range left {
				if isContainer(rightElement) {
					found = jsonbContains(leftElement, rightElement)
				} else {
					found = jsonbScalarEqual(leftElement, rightElement)
				}
				if found {
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		return jsonbScalarEqual(lhs, rhs)
	}
}

func isContainer(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

// jsonbScalarEqual is jsonb scalar equality: the same kind, and equal — two
// numbers when they are numerically equal, whatever their text.
func jsonbScalarEqual(lhs, rhs any) bool {
	switch right := rhs.(type) {
	case nil:
		return lhs == nil
	case string:
		left, ok := lhs.(string)
		return ok && left == right
	case bool:
		left, ok := lhs.(bool)
		return ok && left == right
	case json.Number:
		left, ok := lhs.(json.Number)
		if !ok {
			return false
		}
		l, lok := new(big.Rat).SetString(string(left))
		r, rok := new(big.Rat).SetString(string(right))
		if !lok || !rok {
			return string(left) == string(right)
		}
		return l.Cmp(r) == 0
	default:
		return false
	}
}

// jsonbText is payload ->> key: a string's own text, a number's text, true or
// false, and for an object or array the jsonb text of it. A missing key or a
// JSON null is SQL NULL (ok false).
func jsonbText(payload map[string]any, key string) (string, bool) {
	value, ok := payload[key]
	if !ok || value == nil {
		return "", false
	}
	switch v := value.(type) {
	case string:
		return v, true
	default:
		var buf strings.Builder
		writeJSONBText(&buf, v)
		return buf.String(), true
	}
}

// writeJSONBText renders a value the way jsonb prints it: object keys ordered
// by length and then bytewise, ", " and ": " separators, and strings escaped
// only where JSON requires it.
func writeJSONBText(buf *strings.Builder, value any) {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(v))
	case json.Number:
		buf.WriteString(string(v))
	case string:
		writeJSONBString(buf, v)
	case []any:
		buf.WriteByte('[')
		for i, element := range v {
			if i > 0 {
				buf.WriteString(", ")
			}
			writeJSONBText(buf, element)
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteString(", ")
			}
			writeJSONBString(buf, key)
			buf.WriteString(": ")
			writeJSONBText(buf, v[key])
		}
		buf.WriteByte('}')
	default:
		// Decoded payloads hold nothing else; render defensively as JSON.
		raw, _ := json.Marshal(v)
		buf.Write(raw)
	}
}

// writeJSONBString is Postgres's escape_json.
func writeJSONBString(buf *strings.Builder, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		default:
			if r < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte("0123456789abcdef"[r>>4])
				buf.WriteByte("0123456789abcdef"[r&0xf])
				continue
			}
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// plainNumber is the text a JSON string must have for the numeric reading to
// take it: an optionally negative decimal, as the Postgres expression's regex.
var plainNumber = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// jsonbNumeric is the numeric reading of payload[key]: a JSON number, or a JSON
// string holding a plain decimal, as a double; anything else is NULL.
func jsonbNumeric(payload map[string]any, key string) (float64, bool) {
	switch v := payload[key].(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	case string:
		if !plainNumber.MatchString(v) {
			return 0, false
		}
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
