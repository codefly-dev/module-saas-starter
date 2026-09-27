package testdb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	cliv0 "github.com/codefly-dev/core/generated/go/codefly/cli/v0"
	"github.com/codefly-dev/core/sdk"
	"github.com/stretchr/testify/require"
)

func TestTimingSeparatesSetupFailureFromExecution(t *testing.T) {
	var out bytes.Buffer
	measureSetup(&out, "business-db", []string{"store", "vault"}, time.Minute)(errors.New("boom"))
	var record map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.Equal(t, "dependency-setup", record["phase"])
	require.Equal(t, true, record["failed"])
	require.Equal(t, float64(60000), record["budget_ms"])
	require.Equal(t, []any{"store", "vault"}, record["dependencies"])
	require.NotContains(t, out.String(), "test-execution")
	require.NotContains(t, out.String(), "pending")

	out.Reset()
	measure(&out, "business-db", "test-execution", nil, 0)(false)
	record = nil
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.Equal(t, "test-execution", record["phase"])
	require.Equal(t, false, record["failed"])
	require.NotContains(t, out.String(), "budget_ms")
}

// A readiness timeout is charged to the services still starting, not to the
// requested graph: the ready one is named nowhere, even when the SDK error
// reaches the harness wrapped.
func TestSetupTimeoutNamesTheServicesStillStarting(t *testing.T) {
	var out bytes.Buffer
	timeout := &sdk.ReadinessTimeout{Timeout: time.Minute, Services: []*cliv0.ServiceReadiness{
		{Service: "saas-starter/store", Lifecycle: cliv0.ServiceLifecycle_SERVICE_LIFECYCLE_READY},
		{Service: "saas-starter/vault", Lifecycle: cliv0.ServiceLifecycle_SERVICE_LIFECYCLE_STARTING},
	}}
	measureSetup(&out, "business-db", []string{"store", "vault"}, time.Minute)(fmt.Errorf("start: %w", timeout))
	var record timingRecord
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.True(t, record.Failed)
	require.Equal(t, []string{"saas-starter/vault"}, record.Pending)
	require.Empty(t, record.FailedToStart)
}

func TestSetupFailureNamesTheServicesThatCannotStart(t *testing.T) {
	var out bytes.Buffer
	failure := &sdk.ReadinessFailure{Services: []*cliv0.ServiceReadiness{
		{Service: "saas-starter/store", Lifecycle: cliv0.ServiceLifecycle_SERVICE_LIFECYCLE_FAILED},
	}}
	measureSetup(&out, "infra-db", []string{"store"}, time.Minute)(failure)
	var record timingRecord
	require.NoError(t, json.Unmarshal(out.Bytes(), &record))
	require.True(t, record.Failed)
	require.Equal(t, []string{"saas-starter/store"}, record.FailedToStart)
	require.Empty(t, record.Pending)
}
