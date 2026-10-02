package business

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAuditSinkMode(t *testing.T) {
	for raw, want := range map[string]AuditSinkMode{
		"":           AuditSinkPostgres,
		"postgres":   AuditSinkPostgres,
		" Postgres ": AuditSinkPostgres,
		"both":       AuditSinkBoth,
		"BigQuery":   AuditSinkBigQuery,
	} {
		got, err := ParseAuditSinkMode(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	require.True(t, AuditSinkBigQuery.Swaps())
	require.False(t, AuditSinkPostgres.Swaps())
	require.False(t, AuditSinkBoth.Swaps(), "the tee keeps audit_events as the store of record")

	_, err := ParseAuditSinkMode("external")
	require.ErrorContains(t, err, "not permitted")
	_, err = ParseAuditSinkMode("clickhouse")
	require.ErrorContains(t, err, "must be postgres, both or bigquery", "a swap value with no adapter yet is refused, not ignored")
}

func TestCanonicalAuditDetails(t *testing.T) {
	t.Run("empty payloads are an empty object", func(t *testing.T) {
		for _, payload := range []map[string]any{nil, {}} {
			details, err := CanonicalAuditDetails(payload)
			require.NoError(t, err)
			require.Equal(t, "{}", details)
		}
	})
	t.Run("keys sorted at every level, no whitespace, no HTML escaping", func(t *testing.T) {
		details, err := CanonicalAuditDetails(map[string]any{
			"zeta":  "a<b>&c",
			"alpha": map[string]any{"y": 2, "x": []any{"b", "a"}},
			"mid":   true,
		})
		require.NoError(t, err)
		require.Equal(t, `{"alpha":{"x":["b","a"],"y":2},"mid":true,"zeta":"a<b>&c"}`, details)
	})
	t.Run("the same payload in any order is the same string", func(t *testing.T) {
		first, err := CanonicalAuditDetails(map[string]any{"a": 1, "b": "two", "c": []string{"x"}})
		require.NoError(t, err)
		second, err := CanonicalAuditDetails(map[string]any{"c": []string{"x"}, "b": "two", "a": 1})
		require.NoError(t, err)
		require.Equal(t, first, second)
	})
	t.Run("a payload read back from the queue keeps its number text", func(t *testing.T) {
		decoder := json.NewDecoder(bytes.NewReader([]byte(`{"ratio": 1.50, "count": 9007199254740993}`)))
		decoder.UseNumber()
		var payload map[string]any
		require.NoError(t, decoder.Decode(&payload))
		details, err := CanonicalAuditDetails(payload)
		require.NoError(t, err)
		require.Equal(t, `{"count":9007199254740993,"ratio":1.50}`, details)
	})
	t.Run("an unencodable payload is an error, not a silently emptied record", func(t *testing.T) {
		_, err := CanonicalAuditDetails(map[string]any{"bad": make(chan int)})
		require.Error(t, err)
	})
}

func TestAuditDetailsSHA256IsTheHashOfTheStoredString(t *testing.T) {
	// SHA-256 of "{}", so any reader can check the empty case by hand.
	require.Equal(t, "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", AuditDetailsSHA256("{}"))

	details := `{"provider":"example"}`
	sum := sha256.Sum256([]byte(details))
	require.Equal(t, hex.EncodeToString(sum[:]), AuditDetailsSHA256(details))
}

func TestNewAuditRecord(t *testing.T) {
	entry := AuditEntry{ID: NewIDString(), EventType: EventAuthLogin, Payload: map[string]any{"method": "password"}}
	record, err := NewAuditRecord(entry, RetentionSecurity)
	require.NoError(t, err)
	require.Equal(t, entry, record.Entry)
	require.Equal(t, RetentionSecurity, record.Retention)
	require.Equal(t, `{"method":"password"}`, record.Details)
	require.Equal(t, AuditDetailsSHA256(record.Details), record.DetailsSHA256)

	_, err = NewAuditRecord(AuditEntry{Payload: map[string]any{"bad": func() {}}}, RetentionContent)
	require.Error(t, err)
}
