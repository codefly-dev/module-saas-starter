package main

import (
	"errors"
	"testing"

	"accounts/pkg/datasource/apisource"
	"accounts/pkg/infra"

	"github.com/stretchr/testify/require"
)

// localEgressValue sets `datasource-oauth/local_egress` on both carriers a
// workspace-group read consults, and withdraws any allowance a test installed.
func localEgressValue(t *testing.T, value string) {
	t.Helper()
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__DATASOURCE_OAUTH__LOCAL_EGRESS", value)
	t.Setenv("local_egress", value)
	t.Cleanup(func() { _ = apisource.ConfigureLocalEgress("", false) })
}

// #1087: a deployed composition that sets the allowance refuses to START, through
// the same function main calls, before any other startup refusal can answer in
// its place.
func TestStartupRefusesLocalEgressOutsideTheLocalEnvironment(t *testing.T) {
	localEgressValue(t, "127.0.0.1:18080")
	err := requireStartupConfiguration(t.Context(), false)
	require.ErrorIs(t, err, apisource.ErrLocalEgressOutsideLocal)
	require.Contains(t, err.Error(), "datasource-oauth/local_egress")

	// The environment fact on its own, independent of whether the machine
	// running this test is itself a pod.
	require.ErrorIs(t, configureLocalEgress(false, false), apisource.ErrLocalEgressOutsideLocal)
}

// A local Codefly environment is not enough on its own: the environment is one
// variable whoever renders a deployment sets. A process in a Kubernetes pod is
// refused even when it names itself local.
func TestLocalEgressRefusedInAPodThatNamesItselfLocal(t *testing.T) {
	localEgressValue(t, "127.0.0.1:18080")
	require.ErrorIs(t, configureLocalEgress(true, true), apisource.ErrLocalEgressOutsideLocal)

	// And through main's path, with the pod fact read from the kubelet's own
	// variable rather than handed in.
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	require.True(t, infra.InCluster())
	require.ErrorIs(t, requireStartupConfiguration(t.Context(), true), apisource.ErrLocalEgressOutsideLocal)
}

// The positive control: a local runtime outside any pod installs the allowance.
// Without it, a configureLocalEgress that refused everything would pass both
// tests above.
func TestLocalEgressInstalledOnALocalRuntime(t *testing.T) {
	localEgressValue(t, "127.0.0.1:18080")
	require.NoError(t, configureLocalEgress(true, false))
}

// The default is empty everywhere, so a deployed host with no allowance starts
// exactly as before.
func TestLocalEgressEmptyIsAcceptedEverywhere(t *testing.T) {
	localEgressValue(t, "")
	require.NoError(t, configureLocalEgress(false, true))
}

// A malformed entry refuses even a local start, naming the key.
func TestLocalEgressMalformedRefusesALocalStart(t *testing.T) {
	localEgressValue(t, "localhost:*")
	err := configureLocalEgress(true, false)
	require.True(t, errors.Is(err, apisource.ErrInvalidLocalEgress), "got %v", err)
	require.Contains(t, err.Error(), "datasource-oauth/local_egress")
}
