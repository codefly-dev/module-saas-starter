package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The status surface is routed at the public edge without a session, and the
// product's own /status page renders each component's reason to whoever opens
// it. A probe failure is a driver error: pgx reports a dial failure as
// `failed to connect to host=... user=... database=...`, so returning err.Error()
// here publishes the internal host, port, user and database name to an anonymous
// caller. The reader gets the state; the error goes to the log.
func TestStatusHandlerDoesNotPublishProbeErrorText(t *testing.T) {
	t.Cleanup(resetStatusProbesForTest(t))

	RegisterStatusProbe(StatusProbe{
		Name: "postgres",
		Check: func(context.Context) error {
			return errors.New("failed to connect to host=db.internal user=accounts database=saas: connection refused")
		},
	})

	recorder := httptest.NewRecorder()
	NewStatusHTTPHandler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))

	require.Equal(t, http.StatusOK, recorder.Code, "a degraded component still serves")
	body := recorder.Body.String()
	require.NotContains(t, body, "db.internal")
	require.NotContains(t, body, "user=accounts")
	require.NotContains(t, body, "connection refused")

	var response StatusResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.Equal(t, "degraded", response.Status)
	require.Len(t, response.Components, 1)
	require.Equal(t, "postgres", response.Components[0].Name)
	require.Equal(t, "degraded", response.Components[0].Status)
	require.Equal(t, statusReasonUnavailable, response.Components[0].Error)
}

// A healthy probe reports no reason at all.
func TestStatusHandlerHealthyComponentCarriesNoReason(t *testing.T) {
	t.Cleanup(resetStatusProbesForTest(t))

	RegisterStatusProbe(StatusProbe{Name: "vault", Check: func(context.Context) error { return nil }})

	recorder := httptest.NewRecorder()
	NewStatusHTTPHandler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))

	var response StatusResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "ok", response.Status)
	require.Len(t, response.Components, 1)
	require.Empty(t, response.Components[0].Error)
}

// resetStatusProbesForTest empties the process-wide probe registry and returns
// the restore. The registry is package state shared with every other test in
// this package, so a probe left behind would leak into an unrelated assertion.
func resetStatusProbesForTest(t *testing.T) func() {
	t.Helper()
	statusProbesMu.Lock()
	saved := statusProbes
	statusProbes = nil
	statusProbesMu.Unlock()
	return func() {
		statusProbesMu.Lock()
		statusProbes = saved
		statusProbesMu.Unlock()
	}
}
