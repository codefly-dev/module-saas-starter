package testdb

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	cliv0 "github.com/codefly-dev/core/generated/go/codefly/cli/v0"
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

// A setup record names the requested graph rather than the service that stalled
// because flow status carries one flow-wide flag. Failing here means readiness
// became attributable and these records can name the service that overran.
func TestFlowStatusCarriesNoPerServiceReadiness(t *testing.T) {
	fields := (&cliv0.FlowStatus{}).ProtoReflect().Descriptor().Fields()
	require.Equal(t, 1, fields.Len(), "flow status gained fields: revisit per-service startup attribution")
	require.Equal(t, "ready", string(fields.Get(0).Name()))
}
