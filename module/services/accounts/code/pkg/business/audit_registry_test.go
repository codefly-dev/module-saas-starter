package business

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
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

// The event-type identifier is the durable discriminator producers emit and
// consumers filter/aggregate on exactly (see postgres_audit.go: `event_type = $`).
// It must stay stable across schema revisions: the version lives on a dedicated
// axis — AuditEventDefinition.Version, projected to the audit_events.schema_version
// column and audit_event_types.version — never baked into the type string. A
// version suffix like "saas.auth.login.v1" would orphan every historical row typed
// "saas.auth.login" from exact-match queries and force every consumer to strip it, so
// forbid it here.
func TestAuditCatalog_TypesCarryNoVersionSuffix(t *testing.T) {
	versionSuffix := regexp.MustCompile(`\.v\d+$`)
	for _, d := range auditEventCatalog {
		require.NotRegexp(t, versionSuffix, string(d.Type),
			"event type %q must not encode its version in the identifier string; "+
				"bump AuditEventDefinition.Version instead (schema_version column)", d.Type)
	}
}

// Every type this module mints lives under its own namespace: a composed
// workspace hosts several modules against one audit spine, and a bare
// `<aggregate>.<event>` lets two of them mint the same event_type. The shape is
// the domain-event law from EVENTS.md — `<namespace>.<aggregate>.<event>` —
// with deeper aggregates (saas.datasource.source.added) still legal.
func TestAuditCatalog_TypesAreNamespaced(t *testing.T) {
	namespaced := regexp.MustCompile(`^saas\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	for _, d := range auditEventCatalog {
		require.Regexp(t, namespaced, string(d.Type),
			"event type %q must be <namespace>.<aggregate>.<event>", d.Type)
		require.Equal(t, AuditNamespace, d.Namespace, "event %q carries the wrong namespace", d.Type)
		require.True(t, strings.HasPrefix(string(d.Type), d.Namespace+"."),
			"event %q must start with its declared namespace %q", d.Type, d.Namespace)
	}
}

func TestValidatePayload(t *testing.T) {
	t.Run("unregistered type", func(t *testing.T) {
		require.Error(t, ValidatePayload("saas.nope.not_real", nil))
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
		out := RedactPayload("saas.nope.not_real", map[string]any{"anything": "x"})
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

// The App-installation reason codes are the values consumers filter and
// aggregate on, and they reach the payload through datasourceInstallationReasons
// rather than being written at the emit site. A code added to that map without
// being declared on both events would pass every other gate and then fail
// ValidatePayload only at runtime, where an audit record is never dropped — so
// the drift would surface as a warning in a log, not as a failure.
func TestAuditCatalog_InstallationReasonCodesAreDeclaredOnBothEvents(t *testing.T) {
	declared := func(event EventType, field string) []string {
		d, ok := auditEventIndex[event]
		require.Truef(t, ok, "%q is not registered", event)
		for _, f := range d.Fields {
			if f.Name == field {
				require.Equalf(t, FieldEnum, f.Kind, "%q field %q must be an enum", event, field)
				return f.Enum
			}
		}
		t.Fatalf("%q declares no field %q", event, field)
		return nil
	}

	lost := declared(EventDatasourceSourceAccessLost, "reason")
	restored := declared(EventDatasourceSourceAccessRestored, "restored_from")
	require.ElementsMatch(t, lost, restored,
		"a cause a source can be parked for is a cause it can be restored from; the two enums must agree")

	codes := slices.Sorted(maps.Values(datasourceInstallationReasonCodes))
	require.ElementsMatch(t, codes, lost,
		"every code datasourceInstallationReasonCodes can put in a payload must be declared on the event")
}

// A document producer records its governance actions through EmitAuditEvent,
// which rejects any payload key the type does not declare. These are the exact
// payloads the producer sends — success and refusal alike — so a field it
// relies on can never be silently undeclared here.
func TestValidatePayload_DocumentGovernanceEvents(t *testing.T) {
	accepted := []struct {
		event   EventType
		payload map[string]any
	}{
		{EventDocumentOwnershipTransferred, map[string]any{"solution": "documents", "new_owner_subject_id": "subject-2"}},
		{EventDocumentFrozen, map[string]any{"solution": "documents", "version": "v1"}},
		{EventDocumentUnfrozen, map[string]any{"solution": "documents", "version": "v1"}},
		{EventDocumentGovernanceRefused, map[string]any{"solution": "documents", "action": DocumentGovernanceFreeze, "reason": "entry not found"}},
		{EventDocumentGovernanceRefused, map[string]any{"solution": "documents", "action": DocumentGovernanceOwnershipTransfer, "reason": "not the owner"}},
		{EventDocumentGovernanceRefused, map[string]any{"solution": "documents", "action": DocumentGovernanceUnfreeze, "reason": "not governed"}},
		{EventDocumentGovernanceRefused, map[string]any{"solution": "documents", "version": "v1",
			"action": DocumentGovernanceQuarantineRelease, "reason": "approval named another tenant", "claimed_tenant": "org-b"}},
	}
	for _, c := range accepted {
		require.NoError(t, ValidatePayload(c.event, c.payload), "%s %v", c.event, c.payload)
	}

	// A refusal that does not say what was refused, or why, records that
	// something was denied without recording what — unrepairable on an
	// append-only trail. Absent, empty and whitespace must all be refused: a
	// Required field alone does not reject "  ".
	for _, field := range []string{"action", "reason"} {
		for _, value := range []any{nil, "", "  ", 12} {
			payload := map[string]any{"solution": "documents", "action": DocumentGovernanceFreeze, "reason": "entry not found"}
			if value == nil {
				delete(payload, field)
			} else {
				payload[field] = value
			}
			require.Error(t, ValidatePayload(EventDocumentGovernanceRefused, payload),
				"governance_refused must reject %s=%v", field, value)
		}
	}
	require.Error(t, ValidatePayload(EventDocumentGovernanceRefused, map[string]any{
		"solution": "documents", "action": "exfiltrate", "reason": "why"}),
		"action is one of the registered governance actions")

	// A refusal is its own type, never an outcome on the action's own type: one
	// type cannot carry both a privileged write and an observation, and
	// "was this refused?" must be one predicate rather than a payload value on
	// some types and a type name on others.
	for _, event := range []EventType{EventDocumentOwnershipTransferred, EventDocumentFrozen, EventDocumentUnfrozen} {
		require.Error(t, ValidatePayload(event, map[string]any{"solution": "documents", "outcome": "failure"}),
			"%s must not accept an outcome; a refusal is EventDocumentGovernanceRefused", event)
		require.Error(t, ValidatePayload(event, map[string]any{"solution": "documents", "reason": "not the owner"}),
			"%s must not accept a reason; a refusal is EventDocumentGovernanceRefused", event)
	}
	require.Error(t, ValidatePayload(EventDocumentFrozen, map[string]any{"solution": "documents", "new_owner_subject_id": "subject-2"}),
		"a field declared on one governance event must not leak into another")

	// Freeze and unfreeze are a pair: without the second, the trail cannot say
	// which entries are governed once a producer lifts one.
	for _, event := range []EventType{EventDocumentFrozen, EventDocumentUnfrozen} {
		_, registered := LookupAuditEvent(event)
		require.Truef(t, registered, "%s must be registered: a one-way governance transition is unreadable", event)
	}
}

// claimed_tenant names a tenant other than the one whose trail the row sits on.
// The event exists because an approval named the wrong tenant, so letting that
// identifier reach an export turns the detection into a cross-tenant
// disclosure: RedactPayload feeds the customer's S3 destination, the JSON
// download and the outbound webhook fan-out.
func TestRedactPayload_GovernanceRefusalHidesTheOtherTenant(t *testing.T) {
	redacted := RedactPayload(EventDocumentGovernanceRefused, map[string]any{
		"solution":       "documents",
		"action":         DocumentGovernanceQuarantineRelease,
		"reason":         "approval named another tenant",
		"claimed_tenant": "org-of-another-customer",
	})
	require.NotContains(t, redacted, "claimed_tenant",
		"claimed_tenant identifies another tenant and must never reach an export path")
	require.Equal(t, DocumentGovernanceQuarantineRelease, redacted["action"],
		"the refusal itself must survive redaction; only the other tenant's identity is stripped")
	require.Equal(t, "approval named another tenant", redacted["reason"])
}
