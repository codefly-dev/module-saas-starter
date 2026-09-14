package testdb

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimingSeparatesSetupFailureFromExecution(t *testing.T) {
	var out bytes.Buffer
	measure(&out, "business-db", "dependency-setup", []string{"store", "vault"}, time.Minute)(true)
	var record map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.Equal(t, "dependency-setup", record["phase"])
	require.Equal(t, true, record["failed"])
	require.Equal(t, float64(60000), record["budget_ms"])
	require.Equal(t, []any{"store", "vault"}, record["dependencies"])
	require.NotContains(t, out.String(), "test-execution")

	out.Reset()
	measure(&out, "business-db", "test-execution", nil, 0)(false)
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.Equal(t, "test-execution", record["phase"])
	require.Equal(t, false, record["failed"])
	require.NotContains(t, out.String(), "budget_ms")
}
