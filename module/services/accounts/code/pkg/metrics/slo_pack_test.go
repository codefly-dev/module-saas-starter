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
}
