package analytics_test

import (
	"math"
	"strings"
	"testing"

	"accounts/pkg/analytics"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEventValidationEnforcesRegistryPrivacyAndProperties(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)

	valid, err := registry.NewEvent(analytics.NewEventInput{
		Name:         "onboarding_step_viewed",
		AnonymousID:  uuid.NewString(),
		SessionID:    uuid.NewString(),
		Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
		ConsentState: analyticsv1.ConsentState_CONSENT_STATE_GRANTED,
		Properties:   map[string]any{"step_name": "choose_plan", "flow_version": "v1"},
	})
	require.NoError(t, err)
	require.NoError(t, registry.Validate(valid))

	t.Run("unregistered property", func(t *testing.T) {
		event, err := registry.NewEvent(analytics.NewEventInput{
			Name:         "onboarding_step_viewed",
			AnonymousID:  uuid.NewString(),
			Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
			ConsentState: analyticsv1.ConsentState_CONSENT_STATE_GRANTED,
			Properties:   map[string]any{"step_name": "choose_plan"},
		})
		require.NoError(t, err)
		event.Properties.Fields["plan_name"] = event.Properties.Fields["step_name"]
		require.ErrorContains(t, registry.Validate(event), "not registered")
	})

	t.Run("email value", func(t *testing.T) {
		_, err := registry.NewEvent(analytics.NewEventInput{
			Name:         "onboarding_step_viewed",
			AnonymousID:  uuid.NewString(),
			Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
			ConsentState: analyticsv1.ConsentState_CONSENT_STATE_GRANTED,
			Properties:   map[string]any{"step_name": "person@example.com"},
		})
		require.ErrorContains(t, err, "email addresses")
	})

	t.Run("embedded email value", func(t *testing.T) {
		_, err := registry.NewEvent(analytics.NewEventInput{
			Name:         "onboarding_step_viewed",
			AnonymousID:  uuid.NewString(),
			Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
			ConsentState: analyticsv1.ConsentState_CONSENT_STATE_GRANTED,
			Properties:   map[string]any{"step_name": "contact person@example.com now"},
		})
		require.ErrorContains(t, err, "email addresses")
	})

	t.Run("credential value", func(t *testing.T) {
		_, err := registry.NewEvent(analytics.NewEventInput{
			Name:         "onboarding_step_viewed",
			AnonymousID:  uuid.NewString(),
			Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
			ConsentState: analyticsv1.ConsentState_CONSENT_STATE_GRANTED,
			Properties:   map[string]any{"step_name": "Bearer abcdefghijklmnopqrstuvwxyz"},
		})
		require.ErrorContains(t, err, "resembles a credential")
	})

	t.Run("unbounded text", func(t *testing.T) {
		_, err := registry.NewEvent(analytics.NewEventInput{
			Name:         "onboarding_step_viewed",
			AnonymousID:  uuid.NewString(),
			Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
			ConsentState: analyticsv1.ConsentState_CONSENT_STATE_GRANTED,
			Properties:   map[string]any{"step_name": strings.Repeat("x", 257)},
		})
		require.ErrorContains(t, err, "exceeds 256 bytes")
	})

	t.Run("event-specific property types", func(t *testing.T) {
		_, err := registry.NewEvent(analytics.NewEventInput{
			Name:        "waitlist_joined",
			ActorUserID: uuid.NewString(),
			Source:      analyticsv1.EventSource_EVENT_SOURCE_API,
			Properties:  map[string]any{"referral_present": "yes"},
		})
		require.ErrorContains(t, err, "must be a boolean")
		_, err = registry.NewEvent(analytics.NewEventInput{
			Name:        "survey_responded",
			ActorUserID: uuid.NewString(),
			Source:      analyticsv1.EventSource_EVENT_SOURCE_API,
			Properties:  map[string]any{"score": 4.5},
		})
		require.NoError(t, err)
		_, err = registry.NewEvent(analytics.NewEventInput{
			Name:        "survey_responded",
			ActorUserID: uuid.NewString(),
			Source:      analyticsv1.EventSource_EVENT_SOURCE_API,
			Properties:  map[string]any{"score": math.NaN()},
		})
		require.ErrorContains(t, err, "finite")
	})

	t.Run("route query", func(t *testing.T) {
		_, err := registry.NewEvent(analytics.NewEventInput{
			Name:         "onboarding_step_viewed",
			AnonymousID:  uuid.NewString(),
			Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
			ConsentState: analyticsv1.ConsentState_CONSENT_STATE_GRANTED,
			Context:      &analyticsv1.EventContext{Route: "/welcome?token=secret"},
			Properties:   map[string]any{"step_name": "choose_plan"},
		})
		require.ErrorContains(t, err, "route must not contain")
	})

	t.Run("credential context", func(t *testing.T) {
		_, err := registry.NewEvent(analytics.NewEventInput{
			Name:         "onboarding_step_viewed",
			AnonymousID:  uuid.NewString(),
			Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
			ConsentState: analyticsv1.ConsentState_CONSENT_STATE_GRANTED,
			Context: &analyticsv1.EventContext{
				FeatureFlags: map[string]string{
					"checkout": "Bearer abcdefghijklmnopqrstuvwxyz",
				},
			},
			Properties: map[string]any{"step_name": "choose_plan"},
		})
		require.ErrorContains(t, err, "resembles a credential")
	})
}

func TestOptionalBrowserEventsRequireGrantedConsent(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)

	for _, state := range []analyticsv1.ConsentState{
		analyticsv1.ConsentState_CONSENT_STATE_UNSPECIFIED,
		analyticsv1.ConsentState_CONSENT_STATE_NOT_REQUIRED,
		analyticsv1.ConsentState_CONSENT_STATE_DENIED,
		analyticsv1.ConsentState_CONSENT_STATE_WITHDRAWN,
	} {
		_, err := registry.NewEvent(analytics.NewEventInput{
			Name:         "landing_viewed",
			AnonymousID:  uuid.NewString(),
			Source:       analyticsv1.EventSource_EVENT_SOURCE_WEB,
			ConsentState: state,
			Properties:   map[string]any{"page_kind": "home"},
		})
		require.Error(t, err, state.String())
	}
}

func TestEssentialBrowserEventRecordsConsentAsNotRequired(t *testing.T) {
	registry, err := analytics.ParseRegistry([]byte(`{
		"contract_version": 1,
		"defaults": {
			"schema_version": 1,
			"pii_classification": "pseudonymous",
			"retention_days": 30,
			"property_type": "string"
		},
		"events": [{
			"name": "privacy_settings_saved",
			"owner": "privacy",
			"description": "Privacy settings were persisted.",
			"sources": ["web"],
			"purpose": "essential",
			"properties": []
		}]
	}`))
	require.NoError(t, err)
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:        "privacy_settings_saved",
		AnonymousID: uuid.NewString(),
		Source:      analyticsv1.EventSource_EVENT_SOURCE_WEB,
	})
	require.NoError(t, err)
	require.Equal(
		t,
		analyticsv1.ConsentState_CONSENT_STATE_NOT_REQUIRED,
		event.GetPrivacy().GetConsentState(),
	)
}

func TestMemorySinkIsIdempotentAndDetectsIdentityConflict(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:           "invite_accepted",
		ActorUserID:    uuid.NewString(),
		OrganizationID: uuid.NewString(),
		Source:         analyticsv1.EventSource_EVENT_SOURCE_API,
		Properties:     map[string]any{"role": "member"},
	})
	require.NoError(t, err)
	sink := analytics.NewMemorySink()

	first, err := sink.Capture(t.Context(), event)
	require.NoError(t, err)
	require.False(t, first.Duplicate)
	second, err := sink.Capture(t.Context(), event)
	require.NoError(t, err)
	require.True(t, second.Duplicate)
	require.Len(t, sink.Events(), 1)

	conflict := sink.Events()[0]
	conflict.EventName = "invite_created"
	_, err = sink.Capture(t.Context(), conflict)
	require.ErrorIs(t, err, analytics.ErrEventConflict)
}

func TestBackendEventIdentityCanBeDerivedFromDomainFact(t *testing.T) {
	first := analytics.DeterministicEventID("invite_accepted", "invite-123")
	second := analytics.DeterministicEventID("invite_accepted", "invite-123")
	require.Equal(t, first, second)
	require.NotEqual(
		t,
		first,
		analytics.DeterministicEventID("invite_revoked", "invite-123"),
	)
	require.NoError(t, uuid.Validate(first))
}
