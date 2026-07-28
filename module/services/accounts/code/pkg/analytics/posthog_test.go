package analytics_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"accounts/pkg/analytics"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostHogMapsCanonicalIdentityGroupAndContext(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/batch/", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := analytics.NewPostHog(analytics.PostHogConfig{
		APIKey: "phc_test",
		Host:   server.URL,
	})
	require.NoError(t, err)
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	actorID := uuid.NewString()
	orgID := uuid.NewString()
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:           "core_action_completed",
		ActorUserID:    actorID,
		OrganizationID: orgID,
		SessionID:      uuid.NewString(),
		Source:         analyticsv1.EventSource_EVENT_SOURCE_API,
		Context: &analyticsv1.EventContext{
			Route:        "/projects",
			Release:      "v1.2.3",
			Environment:  "test",
			FeatureFlags: map[string]string{"editor": "treatment"},
		},
		Properties: map[string]any{"action": "publish", "definition_version": "v1"},
	})
	require.NoError(t, err)

	delivery, err := client.Capture(t.Context(), event)
	require.NoError(t, err)
	require.Equal(t, event.GetEventId(), delivery.Reference)

	body := <-requests
	require.Equal(t, "phc_test", body["api_key"])
	batch := body["batch"].([]any)
	captured := batch[0].(map[string]any)
	require.Equal(t, "core_action_completed", captured["event"])
	properties := captured["properties"].(map[string]any)
	require.Equal(t, actorID, properties["distinct_id"])
	require.Equal(t, "/projects", properties["route"])
	require.Equal(t, "treatment", properties["feature_flag.editor"])
	require.Equal(t, orgID, properties["$groups"].(map[string]any)["organization"])
}

func TestPostHogUsesOrganizationAsIdentityForOrganizationOnlyFacts(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := analytics.NewPostHog(analytics.PostHogConfig{
		APIKey: "phc_test",
		Host:   server.URL,
	})
	require.NoError(t, err)
	orgID := uuid.NewString()
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:           "organization_configured",
		OrganizationID: orgID,
		Source:         analyticsv1.EventSource_EVENT_SOURCE_API,
	})
	require.NoError(t, err)
	_, err = client.Capture(t.Context(), event)
	require.NoError(t, err)

	batch := (<-requests)["batch"].([]any)
	properties := batch[0].(map[string]any)["properties"].(map[string]any)
	require.Equal(t, orgID, properties["distinct_id"])
}

func TestPostHogEnforcesHTTPSTimeoutAndLocalHTTP(t *testing.T) {
	_, err := analytics.NewPostHog(analytics.PostHogConfig{
		APIKey: "phc_test",
		Host:   "ftp://localhost",
	})
	require.ErrorContains(t, err, "HTTPS")

	client := &http.Client{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	sink, err := analytics.NewPostHog(analytics.PostHogConfig{
		APIKey:     "phc_test",
		Host:       server.URL,
		Timeout:    time.Millisecond,
		HTTPClient: client,
	})
	require.NoError(t, err)
	_, err = sink.Capture(t.Context(), &analyticsv1.ProductEvent{EventId: uuid.NewString()})
	require.ErrorContains(t, err, "deliver to PostHog")
	require.Zero(t, client.Timeout)
}

func TestPostHogProviderFailureRemainsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := analytics.NewPostHog(analytics.PostHogConfig{
		APIKey: "phc_test",
		Host:   server.URL,
	})
	require.NoError(t, err)
	_, err = client.Capture(t.Context(), &analyticsv1.ProductEvent{EventId: uuid.NewString()})
	require.ErrorContains(t, err, "HTTP 503")
}
