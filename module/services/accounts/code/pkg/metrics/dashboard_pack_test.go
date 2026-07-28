package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultDashboardPack(t *testing.T) {
	pack, err := DefaultDashboardPack()
	require.NoError(t, err)
	require.Equal(t, uint32(1), pack.Version)

	dashboards := map[string]bool{}
	for _, dashboard := range pack.Dashboards {
		dashboards[dashboard.ID] = true
		require.NotEmpty(t, dashboard.Owner)
		require.Equal(t, "UTC", dashboard.Timezone)
		require.Positive(t, dashboard.RefreshSeconds)
		for _, metric := range dashboard.Metrics {
			require.NotEmpty(t, metric.Definition)
			require.NotEmpty(t, metric.Source)
		}
	}
	require.Equal(t, map[string]bool{
		"founder_pulse":            true,
		"acquisition":              true,
		"onboarding":               true,
		"product_adoption":         true,
		"retention_churn":          true,
		"revenue":                  true,
		"usage_entitlement":        true,
		"reliability_data_quality": true,
	}, dashboards)
}

func TestDashboardPackRejectsMissingProvenance(t *testing.T) {
	_, err := ParseDashboardPack([]byte(`{
		"version": 1,
		"dashboards": [{
			"id": "revenue",
			"title": "Revenue",
			"owner": "finance",
			"timezone": "UTC",
			"refresh_seconds": 60,
			"metrics": [{"key": "mrr", "title": "MRR", "definition": "Recurring revenue", "source": ""}]
		}]
	}`))
	require.ErrorContains(t, err, "incomplete metric metadata")
}
