package analytics_test

import (
	"testing"

	"accounts/pkg/analytics"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"github.com/stretchr/testify/require"
)

func TestDefaultRegistryOwnsCanonicalTaxonomy(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	require.Equal(t, uint32(1), registry.ContractVersion())

	required := []string{
		"landing_viewed",
		"account_created",
		"organization_created",
		"invite_accepted",
		"onboarding_completed",
		"activation_achieved",
		"core_action_completed",
		"subscription_changed",
		"feedback_submitted",
		"notification_delivered",
	}
	for _, name := range required {
		definition, ok := registry.Definition(name)
		require.Truef(t, ok, "%s must be registered", name)
		require.NotEmpty(t, definition.Owner)
		require.NotEmpty(t, definition.Description)
		require.Positive(t, definition.SchemaVersion)
		require.Positive(t, definition.RetentionDays)
		require.NotEqual(t, analyticsv1.AnalyticsPurpose_ANALYTICS_PURPOSE_UNSPECIFIED, definition.Purpose)
		require.NotEmpty(t, definition.Sources)
	}
	survey, ok := registry.Definition("survey_responded")
	require.True(t, ok)
	require.Equal(t, analytics.PropertyTypeNumber, survey.PropertyTypes["score"])
	waitlist, ok := registry.Definition("waitlist_joined")
	require.True(t, ok)
	require.Equal(
		t,
		analytics.PropertyTypeBoolean,
		waitlist.PropertyTypes["referral_present"],
	)
	require.Len(t, registry.Definitions(), 52)
}

func TestRegistryRejectsUnsafeAndAmbiguousDefinitions(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "duplicate event",
			body: `{"contract_version":1,"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},"events":[
				{"name":"fact_completed","owner":"product","description":"first","sources":["api"],"purpose":"product","properties":[]},
				{"name":"fact_completed","owner":"product","description":"second","sources":["api"],"purpose":"product","properties":[]}
			]}`,
		},
		{
			name: "forbidden property",
			body: `{"contract_version":1,"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},"events":[
				{"name":"fact_completed","owner":"product","description":"fact","sources":["api"],"purpose":"product","properties":["access_token"]}
			]}`,
		},
		{
			name: "unknown source",
			body: `{"contract_version":1,"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},"events":[
				{"name":"fact_completed","owner":"product","description":"fact","sources":["desktop"],"purpose":"product","properties":[]}
			]}`,
		},
		{
			name: "unknown property type",
			body: `{"contract_version":1,"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"object"},"events":[
				{"name":"fact_completed","owner":"product","description":"fact","sources":["api"],"purpose":"product","properties":[]}
			]}`,
		},
		{
			name: "unused property override",
			body: `{"contract_version":1,"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},"property_types":{"score":"number"},"events":[
				{"name":"fact_completed","owner":"product","description":"fact","sources":["api"],"purpose":"product","properties":[]}
			]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := analytics.ParseRegistry([]byte(test.body))
			require.Error(t, err)
		})
	}
}

func TestRegistryCompatibilityRequiresVersionsForBreakingChanges(t *testing.T) {
	previous := mustParseRegistry(t, `{
		"contract_version":1,
		"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},
		"events":[{"name":"fact_completed","owner":"product","description":"fact","sources":["api"],"purpose":"product","properties":["kind"]}]
	}`)
	additive := mustParseRegistry(t, `{
		"contract_version":1,
		"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},
		"events":[{"name":"fact_completed","owner":"product","description":"fact","sources":["api"],"purpose":"product","properties":["kind","variant"]}]
	}`)
	require.NoError(t, analytics.CheckCompatible(previous, additive))

	removedProperty := mustParseRegistry(t, `{
		"contract_version":1,
		"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},
		"events":[{"name":"fact_completed","owner":"product","description":"fact","sources":["api"],"purpose":"product","properties":[]}]
	}`)
	require.ErrorContains(
		t,
		analytics.CheckCompatible(previous, removedProperty),
		"removed property",
	)

	changedSource := mustParseRegistry(t, `{
		"contract_version":1,
		"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},
		"events":[{"name":"fact_completed","owner":"product","description":"fact","sources":["worker"],"purpose":"product","properties":["kind"]}]
	}`)
	require.ErrorContains(
		t,
		analytics.CheckCompatible(previous, changedSource),
		"without a schema version",
	)

	versionedBreakingChange := mustParseRegistry(t, `{
		"contract_version":2,
		"defaults":{"schema_version":2,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},
		"events":[{"name":"fact_completed","owner":"product","description":"fact","sources":["worker"],"purpose":"product","properties":[]}]
	}`)
	require.NoError(t, analytics.CheckCompatible(previous, versionedBreakingChange))
}

func TestRegistryVersionsEventsIndependently(t *testing.T) {
	registry := mustParseRegistry(t, `{
		"contract_version":1,
		"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},
		"events":[
			{"name":"first_completed","owner":"product","description":"first","schema_version":2,"sources":["api"],"purpose":"product","properties":[]},
			{"name":"second_completed","owner":"product","description":"second","sources":["api"],"purpose":"product","properties":[]}
		]
	}`)

	first, ok := registry.Definition("first_completed")
	require.True(t, ok)
	second, ok := registry.Definition("second_completed")
	require.True(t, ok)
	require.Equal(t, uint32(2), first.SchemaVersion)
	require.Equal(t, uint32(1), second.SchemaVersion)
}

func TestDefaultRegistryIsCompatibleWithTheFullVersionOneTaxonomy(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	require.Len(t, registry.Definitions(), 52)
}

func mustParseRegistry(t *testing.T, body string) *analytics.Registry {
	t.Helper()
	registry, err := analytics.ParseRegistry([]byte(body))
	require.NoError(t, err)
	return registry
}
