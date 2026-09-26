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
	// The removed-datasource payload names the provider, and the column it is
	// read from is NOT NULL under a CHECK over exactly these four values. A
	// string field could not refuse a fifth, so the check the database makes
	// would stop at the trail's edge.
	t.Run("removed datasource provider is checked against the stored set", func(t *testing.T) {
		for _, provider := range []string{
			DatasourceProviderGitHub, DatasourceProviderAPI,
			DatasourceProviderCrawler, DatasourceProviderUpload,
		} {
			require.NoError(t, ValidatePayload(EventDatasourceSourceRemoved, map[string]any{
				"provider": provider, "repo": "acme/docs", "boundary": "node-1",
			}), "provider %q is storable, so it must validate", provider)
		}
		err := ValidatePayload(EventDatasourceSourceRemoved, map[string]any{"provider": "carrier_pigeon"})
		require.ErrorContains(t, err, "enum")
	})
	t.Run("string array accepts []any", func(t *testing.T) {
		require.NoError(t, ValidatePayload(EventAPIKeyCreated, map[string]any{
			"key_id": "00000000-0000-0000-0000-000000000001",
			"scopes": []any{"a", "b"},
		}))
	})
}

func TestResolvedAuditEvent_Redact(t *testing.T) {
	t.Run("strips PII fields", func(t *testing.T) {
		out := catalogResolved(EventUserRegistered).Redact(map[string]any{
			"signup_method": "password",
			"email":         "secret@example.com",
		})
		require.Equal(t, map[string]any{"signup_method": "password"}, out)
	})
	t.Run("no-PII type passes through", func(t *testing.T) {
		in := map[string]any{"step": "invite_team"}
		require.Equal(t, in, catalogResolved(EventOnboardingStepDone).Redact(in))
	})
	t.Run("unregistered type fails closed", func(t *testing.T) {
		out := catalogResolved("saas.nope.not_real").Redact(map[string]any{"anything": "x"})
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

// A document module records each re-queued dead letter with exactly this
// payload; EmitAuditEvent rejects an undeclared key, so the declaration has to
// match what the producer sends, and every field that says what was re-run is
// required.
func TestValidatePayload_DocumentDeadLetterRedriven(t *testing.T) {
	complete := func() map[string]any {
		return map[string]any{
			"solution": "documents", "version": "v1", "correlation_id": "redrive-7",
			"producer": "embed", "error_class": "exhausted",
		}
	}
	require.NoError(t, ValidatePayload(EventDocumentDeadLetterRedriven, complete()))

	// The vocabulary covers the whole space, so a producer never has to file a
	// run it cannot classify as one it can.
	for _, class := range []string{"permanent", "exhausted", "cancelled", "unknown"} {
		payload := complete()
		payload["error_class"] = class
		require.NoErrorf(t, ValidatePayload(EventDocumentDeadLetterRedriven, payload), "error_class %q", class)
	}

	require.Error(t, ValidatePayload(EventDocumentDeadLetterRedriven, func() map[string]any {
		p := complete()
		p["error_class"] = "flaky"
		return p
	}()), "error_class is a closed vocabulary")

	require.Error(t, ValidatePayload(EventDocumentDeadLetterRedriven, func() map[string]any {
		p := complete()
		p["last_error"] = "x"
		return p
	}()), "the error text is never on the spine")

	// Every identifying field, absent and blank. A required string that accepts
	// "" writes a row that reads as a complete record and names nothing: the
	// zero value of a Go string is what an emitter sends when it forgot to set
	// the field, which is exactly when the record has to refuse.
	for _, field := range []string{"version", "correlation_id", "producer", "error_class"} {
		for _, blank := range []any{nil, "", "   ", 12} {
			payload := complete()
			if blank == nil {
				delete(payload, field)
			} else {
				payload[field] = blank
			}
			require.Errorf(t, ValidatePayload(EventDocumentDeadLetterRedriven, payload),
				"%s=%v must be refused", field, blank)
		}
	}

	// producer is bounded so it can name a stage and not carry a transcript.
	overLong := complete()
	overLong["producer"] = strings.Repeat("e", 129)
	require.Error(t, ValidatePayload(EventDocumentDeadLetterRedriven, overLong),
		"producer is bounded at 128 bytes")
	atBound := complete()
	atBound["producer"] = strings.Repeat("e", 128)
	require.NoError(t, ValidatePayload(EventDocumentDeadLetterRedriven, atBound))
}

// The nonempty rule belongs to the declaration, not to a list of the event types
// somebody remembered to add: a required string-valued field on ANY registered
// type refuses a blank, and a required bool or int still records false and 0.
func TestValidatePayload_RequiredStringFieldsRejectBlanksOnEveryEvent(t *testing.T) {
	checked := 0
	for _, d := range AuditEventCatalog() {
		for _, f := range d.Fields {
			if !f.Required {
				continue
			}
			switch f.Kind {
			case FieldString, FieldUUID, FieldEnum:
				require.Errorf(t, validateField(d.Type, f, ""), "%s.%s accepted an empty string", d.Type, f.Name)
				require.Errorf(t, validateField(d.Type, f, "  "), "%s.%s accepted a blank string", d.Type, f.Name)
				checked++
			case FieldBool:
				require.NoErrorf(t, validateField(d.Type, f, false), "%s.%s must still record false", d.Type, f.Name)
				checked++
			case FieldInt:
				require.NoErrorf(t, validateField(d.Type, f, 0), "%s.%s must still record zero", d.Type, f.Name)
				checked++
			}
		}
	}
	require.NotZero(t, checked, "no required fields found; the catalog walk is broken")
}

// A type whose record is about a resource must name one. The registry decides
// it, because the request field it constrains is shared with every type that has
// no entry to name.
func TestAuditCatalog_EntryRequirementIsDeclaredNotAssumed(t *testing.T) {
	require.True(t, AuditEventRequiresEntry(EventDocumentDeadLetterRedriven))
	require.False(t, AuditEventRequiresEntry(EventDocumentSearch),
		"a search is about a collection, not an entry")
	require.False(t, AuditEventRequiresEntry("saas.not.registered"),
		"an unregistered type is refused as unregistered, not as missing an entry")

	require.True(t, AuditEventRequiresIdempotencyKey(EventDocumentDeadLetterRedriven),
		"a per-item queue report retries, so its emitter must name the operation")
	require.False(t, AuditEventRequiresIdempotencyKey(EventDocumentIngested),
		"the requirement belongs to the types that declared it, not to the surface")
	require.False(t, AuditEventRequiresIdempotencyKey("saas.not.registered"))
}
