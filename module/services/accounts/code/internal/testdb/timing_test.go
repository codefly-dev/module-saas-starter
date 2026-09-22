package testdb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	cliv0 "github.com/codefly-dev/core/generated/go/codefly/cli/v0"
	"github.com/codefly-dev/core/sdk"
	"github.com/stretchr/testify/require"
)

func TestTimingSeparatesSetupFailureFromExecution(t *testing.T) {
	var out bytes.Buffer
	measure(&out, "business-db", "dependency-setup", []string{"store", "vault"}, time.Minute)(true, nil)
	var record map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.Equal(t, "dependency-setup", record["phase"])
	require.Equal(t, true, record["failed"])
	require.Equal(t, float64(60000), record["budget_ms"])
	require.Equal(t, []any{"store", "vault"}, record["dependencies"])
	require.NotContains(t, out.String(), "test-execution")

	out.Reset()
	measure(&out, "business-db", "test-execution", nil, 0)(false, nil)
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.Equal(t, "test-execution", record["phase"])
	require.Equal(t, false, record["failed"])
	require.NotContains(t, out.String(), "budget_ms")
}

func TestTimingNamesOnlyTheBlockedDependencies(t *testing.T) {
	for _, setupErr := range []error{
		fmt.Errorf("setup: %w", &sdk.ReadinessTimeout{Timeout: time.Minute, Services: []*cliv0.ServiceReadiness{
			{Service: "app/store", Lifecycle: cliv0.ServiceLifecycle_SERVICE_LIFECYCLE_READY},
			{Service: "app/vault", Lifecycle: cliv0.ServiceLifecycle_SERVICE_LIFECYCLE_STARTING},
		}}),
		&sdk.ReadinessFailure{Services: []*cliv0.ServiceReadiness{
			{Service: "app/vault", Lifecycle: cliv0.ServiceLifecycle_SERVICE_LIFECYCLE_FAILED},
		}},
	} {
		var out bytes.Buffer
		measure(&out, "business-db", "dependency-setup", []string{"store", "vault"}, time.Minute)(true, setupErr)
		var record struct {
			Blocked []struct {
				Service   string `json:"service"`
				Lifecycle string `json:"lifecycle"`
			} `json:"blocked_services"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &record))
		require.Len(t, record.Blocked, 1)
		require.Equal(t, "app/vault", record.Blocked[0].Service)
		require.NotEmpty(t, record.Blocked[0].Lifecycle)
	}
}
