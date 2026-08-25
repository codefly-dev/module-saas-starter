package business

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuditCatalog_NoDuplicatesAndComplete(t *testing.T) {
	seen := map[EventType]bool{}
	for _, d := range auditEventCatalog {
		require.NotEmpty(t, d.Type, "event type must be named")
		require.False(t, seen[d.Type], "duplicate event type %q", d.Type)
		seen[d.Type] = true
		require.NotEmpty(t, d.Category, "event %q needs a category", d.Type)
		require.NotEmpty(t, d.Owner, "event %q needs an owner", d.Type)
		require.Positive(t, d.Version, "event %q needs a version", d.Type)
	}
}

func TestValidatePayload(t *testing.T) {
	t.Run("unregistered type", func(t *testing.T) {
		require.Error(t, ValidatePayload("nope.not_real", nil))
	})
	t.Run("empty payload for optional-field type", func(t *testing.T) {
		require.NoError(t, ValidatePayload(EventUserUpdated, nil))
	})
	t.Run("valid enum + string", func(t *testing.T) {
		require.NoError(t, ValidatePayload(EventUserRegistered, map[string]any{
			"signup_method": "sso",
			"email":         "a@b.com",
		}))
	})
	t.Run("bad enum value", func(t *testing.T) {
		err := ValidatePayload(EventUserRegistered, map[string]any{"signup_method": "carrier_pigeon"})
		require.ErrorContains(t, err, "enum")
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		err := ValidatePayload(EventUserRegistered, map[string]any{"nonsense": "x"})
		require.ErrorContains(t, err, "no registered field")
	})
	t.Run("wrong kind", func(t *testing.T) {
		err := ValidatePayload(EventUserRegistered, map[string]any{"email": 42})
		require.ErrorContains(t, err, "string")
	})
	t.Run("string array accepts []any", func(t *testing.T) {
		require.NoError(t, ValidatePayload(EventAPIKeyCreated, map[string]any{
			"key_id": "00000000-0000-0000-0000-000000000001",
			"scopes": []any{"a", "b"},
		}))
	})
}

func TestRedactPayload(t *testing.T) {
	t.Run("strips PII fields", func(t *testing.T) {
		out := RedactPayload(EventUserRegistered, map[string]any{
			"signup_method": "password",
			"email":         "secret@example.com",
		})
		require.Equal(t, map[string]any{"signup_method": "password"}, out)
	})
	t.Run("no-PII type passes through", func(t *testing.T) {
		in := map[string]any{"step": "invite_team"}
		require.Equal(t, in, RedactPayload(EventOnboardingStepDone, in))
	})
	t.Run("unregistered type fails closed", func(t *testing.T) {
		out := RedactPayload("nope.not_real", map[string]any{"anything": "x"})
		require.Empty(t, out)
	})
}

func TestPayloadSchemaJSON_IsValidJSON(t *testing.T) {
	for _, d := range auditEventCatalog {
		var m map[string]any
		require.NoError(t, json.Unmarshal(d.PayloadSchemaJSON(), &m), "event %q schema must marshal", d.Type)
		require.Equal(t, "object", m["type"])
	}
}
