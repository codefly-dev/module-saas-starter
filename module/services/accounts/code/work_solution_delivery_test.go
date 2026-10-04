package main

import (
	"os"
	"strings"
	"testing"

	"accounts/pkg/infra"

	"github.com/stretchr/testify/require"
)

// The declared-presence surface's BOOT path (issue #952).
//
// These drive `configuredSolutionHostBindingReconciler` itself rather than
// reading work.go, because every one of them is a refusal that has to happen
// before the service is wired — and a refusal that is only asserted in prose is
// a refusal that stops happening the first time the configuration block is
// reordered.

// clearSolutionHostEnvironment empties every source workspaceEnv reads the
// federation group from, so an ambient value cannot decide a case.
func clearSolutionHostEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SOLUTION_HOST_COORDINATE",
		"SOLUTION_HOST_OWNERSHIP_DOMAINS",
		"SOLUTION_HOST_TRUST_POLICY",
		"SOLUTION_HOST_BINDING_INTERVAL",
		// The deleted setting. Cleared too, so a case cannot be decided by an
		// ambient value for a key nothing is allowed to read any more.
		"SOLUTION_HOST_BINDINGS_DIR",
	} {
		t.Setenv(key, "")
		t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__FEDERATION__"+key, "")
		t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__FEDERATION__"+key, "")
	}
}

func setSolutionHost(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__FEDERATION__"+key, value)
}

// NO COORDINATE, NO SURFACE. The coordinate is the one declaration that turns
// declared presence on, and without it there is no reconciler, no delivery
// endpoint and no boot requirement — which is what lets a laptop run the graph
// with every solution present because it heartbeats.
func TestNoCoordinateMeansNoDeclaredPresenceSurface(t *testing.T) {
	clearSolutionHostEnvironment(t)
	// Set everything else, including the deleted mount setting. None of it may
	// bring the surface up on its own.
	setSolutionHost(t, "SOLUTION_HOST_OWNERSHIP_DOMAINS", "acme")
	setSolutionHost(t, "SOLUTION_HOST_TRUST_POLICY", "keyless")
	setSolutionHost(t, "SOLUTION_HOST_BINDINGS_DIR", t.TempDir())

	reconciler, err := configuredSolutionHostBindingReconciler(nil, nil)
	require.NoError(t, err)
	require.Nil(t, reconciler)
}

// A COORDINATE WITH NO OWNERSHIP DOMAINS REFUSES TO BOOT. Core refuses a
// document from an unstated domain, and the reason it must be stated is that the
// applied record cannot bound a binding's FIRST generation: with no declaration,
// any delivery could claim an unseen binding ID under a domain of its own
// choosing and own it from then on.
func TestACoordinateWithoutOwnershipDomainsRefusesToBoot(t *testing.T) {
	clearSolutionHostEnvironment(t)
	setSolutionHost(t, "SOLUTION_HOST_COORDINATE", "acme/prod/eu-west-1")
	setSolutionHost(t, "SOLUTION_HOST_TRUST_POLICY", "keyless")

	_, err := configuredSolutionHostBindingReconciler(nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SOLUTION_HOST_OWNERSHIP_DOMAINS")
}

// AN ABSENT TRUST ANCHOR REFUSES THE BOOT, and the refusal names the anchor.
//
// This is the whole of the host's verification story in one assertion: a host
// that cannot verify a delivered carrier must not start. Refusing per document
// instead would make "this host has no trust root" and "delivery is shipping
// something bad" the same observable, and those are the two facts an operator
// most needs to tell apart.
//
// It is asserted BEFORE the trust-policy value is read, which is the ordering
// half. An unmounted anchor used to be reported as a missing
// SOLUTION_HOST_TRUST_POLICY — a message that sends an operator to edit
// configuration for a problem that is a pod spec.
func TestAnAbsentTrustAnchorRefusesTheBoot(t *testing.T) {
	requireTrustAnchorAbsent(t)
	clearSolutionHostEnvironment(t)
	setSolutionHost(t, "SOLUTION_HOST_COORDINATE", "acme/prod/eu-west-1")
	setSolutionHost(t, "SOLUTION_HOST_OWNERSHIP_DOMAINS", "acme")
	// Deliberately NOT set, so that a refusal naming the policy rather than the
	// anchor fails this test. The anchor is the more fundamental absence and the
	// one the message must name.
	_, err := configuredSolutionHostBindingReconciler(nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), infra.SolutionHostTrustAnchorPath,
		"the refusal must name the anchor that is missing, not the configuration value read after it")
	require.NotContains(t, err.Error(), "SOLUTION_HOST_TRUST_POLICY",
		"an unmounted anchor reported as a missing policy setting sends an operator to the wrong place")

	// And with the policy declared, the refusal is the same one: the anchor's
	// absence is not something a configuration value can satisfy.
	setSolutionHost(t, "SOLUTION_HOST_TRUST_POLICY", "keyless")
	_, err = configuredSolutionHostBindingReconciler(nil, nil)
	require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
	require.Contains(t, err.Error(), infra.SolutionHostTrustAnchorPath)
}

// THE MOUNT SETTING IS GONE FROM THE CODE, not merely unused.
//
// A mechanical check, because the gate it replaces was the defect: while
// SOLUTION_HOST_BINDINGS_DIR existed, an unset value meant no reconciler, no
// verifier and no delivery endpoint, and a set value meant the reconciler's
// source was the directory — so the durable inbox the delivery endpoint writes
// was read by NOTHING in either configuration. Re-adding the setting is a
// one-line change that reads like restoring local development and silently
// restores a mount as the desired set, with the retry gap and the dropped
// carrier that cost.
func TestTheBindingsMountSettingIsNotRead(t *testing.T) {
	source, err := os.ReadFile("work.go")
	require.NoError(t, err)
	require.NotContains(t, string(source), "SOLUTION_HOST_BINDINGS_DIR",
		"the mount is gone: the reconciler's source is the durable inbox, unconditionally")

	// And the reader itself is gone, so there is nothing for a future edit to
	// point at. A constructor left in place is a mount one line away.
	for _, name := range []string{
		"pkg/infra/solution_host_binding_mount.go",
		"pkg/infra/solution_host_binding_mount_test.go",
	} {
		_, err := os.Stat(name)
		require.True(t, os.IsNotExist(err), "%s still exists", name)
	}
}

// requireTrustAnchorAbsent skips rather than lies when a machine happens to have
// the anchor mounted.
//
// The honest statement of what these two cases prove: the ABSENT case is
// exercised here, and the present case is not, because the anchor's path is a
// constant under /etc that a test must not be able to write — which is the
// property that makes it a constant. The present path is exercised against an
// in-process Sigstore in solution_host_keyless_verifier_test.go.
func requireTrustAnchorAbsent(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(infra.SolutionHostTrustAnchorPath); err == nil {
		t.Skipf("%s exists on this machine, so the absent-anchor refusal cannot be exercised here",
			infra.SolutionHostTrustAnchorPath)
	}
}

// The anchor check itself, at its own seam: the sentinel and the path.
func TestRequireSolutionHostTrustAnchorNamesWhatIsMissing(t *testing.T) {
	requireTrustAnchorAbsent(t)
	err := infra.RequireSolutionHostTrustAnchor()
	require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
	require.True(t, strings.Contains(err.Error(), infra.SolutionHostTrustAnchorPath))
}
