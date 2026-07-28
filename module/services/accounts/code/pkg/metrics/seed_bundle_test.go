package metrics

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteDefaultSeedBundleProducesDeployableDefinitions(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, WriteDefaultSeedBundle(&output))

	var bundle SeedBundle
	require.NoError(t, json.Unmarshal(output.Bytes(), &bundle))
	require.NotEmpty(t, bundle.DashboardPack.Dashboards)
	require.NotEmpty(t, bundle.SLOPack.Alerts)
	require.Contains(t, bundle.WarehouseSchemaSQL, "CREATE TABLE IF NOT EXISTS measurement.metric_values")
}
