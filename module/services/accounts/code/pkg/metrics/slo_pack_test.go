package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultSLOPack(t *testing.T) {
	pack, err := DefaultSLOPack()
	require.NoError(t, err)
	require.Equal(t, uint32(1), pack.Version)
	require.Equal(t, 30, pack.WindowDays)
	require.Len(t, pack.SLOs, 8)
	require.GreaterOrEqual(t, len(pack.Alerts), 7)

	ids := map[string]bool{}
	for _, slo := range pack.SLOs {
		ids[slo.ID] = true
	}
	for _, required := range []string{
		"signup",
		"login",
		"invite_acceptance",
		"checkout",
		"core_action",
		"notification_delivery",
		"analytics_export",
		"usage_consumption",
	} {
		require.True(t, ids[required])
	}
	for _, alert := range pack.Alerts {
		require.Equal(t, "promql", alert.ExpressionLanguage)
		require.NotEmpty(t, alert.Expression)
	}
}

func TestSLOPackRejectsAlertWithoutExecutableExpression(t *testing.T) {
	_, err := ParseSLOPack([]byte(`{
		"version": 1,
		"window_days": 30,
		"slos": [{
			"id": "signup",
			"owner": "growth",
			"availability_target": 0.995,
			"latency_p95_ms": 1500,
			"success": "account created",
			"population": "valid signups"
		}],
		"alerts": [{
			"id": "signup_burn",
			"signal": "error_budget_burn",
			"condition": "burn rate too high",
			"expression_language": "promql",
			"expression": "",
			"runbook": "slo-burn"
		}]
	}`))
	require.ErrorContains(t, err, "alert definition is incomplete")
}
