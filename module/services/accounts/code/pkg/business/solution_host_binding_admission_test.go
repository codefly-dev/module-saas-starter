package business

import (
	"context"
	"testing"

	"accounts/internal/deliverytest"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// Admission of delivered manifests without a database: core's Admit and
// CheckAlias against the host's applied records, read into what the pass
// applies and what it exposes as withheld. The Postgres half — row locks, the
// registry's unique key, restart — is solution_host_bindings_integration_test.go.

func admit(t *testing.T, records []*SolutionHostBindingRecord, manifests ...*solutionhost.ModuleManifest) solutionHostBindingAdmission {
	t.Helper()
	return admitSolutionHostBindings(testDeliveryCoordinate, records, delivered(t, manifests...))
}

func TestANewBindingAndAHigherGenerationAreAccepted(t *testing.T) {
	applied := fixtureManifest(t, "reports", 1)
	next := fixtureManifest(t, "reports", 2)
	next.Release.Version = "1.1.0"
	admission := admit(t, []*SolutionHostBindingRecord{appliedRecordOf(t, applied)}, next, fixtureManifest(t, "billing", 1))
	require.Empty(t, admission.Withheld)
	require.Len(t, admission.Accepted, 2)
	for _, accepted := range admission.Accepted {
		require.Equal(t, solutionhost.DecisionAccept, accepted.Decision)
	}
}

func TestTheAppliedGenerationIsAReplay(t *testing.T) {
	applied := fixtureManifest(t, "reports", 3)
	admission := admit(t, []*SolutionHostBindingRecord{appliedRecordOf(t, applied)}, applied)
	require.Empty(t, admission.Withheld)
	require.Len(t, admission.Accepted, 1)
	require.Equal(t, solutionhost.DecisionReplay, admission.Accepted[0].Decision)
}

// Each refusal is withheld for its own binding with core's reason, and leaves
// every other binding's admission alone.
func TestARefusedManifestIsWithheldWithItsReason(t *testing.T) {
	applied := fixtureManifest(t, "reports", 3)
	rewritten := fixtureManifest(t, "reports", 3)
	rewritten.Release.Version = "9.9.9"
	elsewhere := fixtureManifest(t, "reports", 4)
	elsewhere.Host.Coordinate = "another-host"
	reserved := fixtureManifest(t, "codefly", 1)
	held := fixtureManifest(t, "reports", 1)
	held.Binding = "acme.test.second-reports"

	withdrawn := fixtureManifest(t, "archive", 1)
	tombstone := withdrawalOf(t, withdrawn, 2)
	revived := fixtureManifest(t, "archive", 3)

	records := []*SolutionHostBindingRecord{appliedRecordOf(t, applied), appliedRecordOf(t, tombstone)}
	for name, tc := range map[string]struct {
		candidate *solutionhost.ModuleManifest
		want      error
	}{
		"a stale generation":             {fixtureManifest(t, "reports", 2), solutionhost.ErrStaleGeneration},
		"a rewritten generation":         {rewritten, solutionhost.ErrRewrittenGeneration},
		"another host's":                 {elsewhere, solutionhost.ErrWrongHost},
		"a withdrawn binding back":       {revived, solutionhost.ErrWithdrawn},
		"a reserved alias":               {reserved, nil},
		"an alias another binding holds": {held, nil},
	} {
		t.Run(name, func(t *testing.T) {
			sibling := fixtureManifest(t, "billing", 1)
			admission := admit(t, records, tc.candidate, sibling)
			reason, withheld := admission.Withheld[tc.candidate.Binding]
			require.True(t, withheld)
			if tc.want != nil {
				require.Contains(t, reason, tc.want.Error())
			}
			require.Len(t, admission.Accepted, 1, "the sibling is admitted regardless")
			require.Equal(t, sibling.Binding, admission.Accepted[0].Document.Binding)
		})
	}
}

// A withdrawal recorded before this host read module manifests cannot be read
// as one, and it is still terminal.
func TestAPreManifestWithdrawalStaysTerminal(t *testing.T) {
	record := &SolutionHostBindingRecord{BindingID: "acme.test.archive", Applied: &SolutionHostBindingApplied{
		SolutionHostBindingGeneration: SolutionHostBindingGeneration{Generation: 4, Document: "schema: codefly/solution-host-binding/v3\n"},
		Removed:                       true,
	}}
	_, err := record.AppliedManifest()
	require.ErrorIs(t, err, solutionhost.ErrWithdrawn)
	admission := admit(t, []*SolutionHostBindingRecord{record}, fixtureManifest(t, "archive", 5))
	require.Contains(t, admission.Withheld["acme.test.archive"], solutionhost.ErrWithdrawn.Error())

	record.Applied.Removed = false
	_, err = record.AppliedManifest()
	require.ErrorIs(t, err, solutionhost.ErrAppliedUnusable, "a live record nobody can read is the host's own damage")
}

// A route alias handed from one binding to another releases before it is
// claimed, so the hand-over completes in one pass.
func TestAppliesReleaseAnAliasBeforeTheyClaimIt(t *testing.T) {
	leaving := fixtureManifest(t, "reports", 1)
	records := []*SolutionHostBindingRecord{appliedRecordOf(t, leaving)}
	claimant := fixtureManifest(t, "reports", 1)
	claimant.Binding = "acme.test.reports-next"
	ordered := orderSolutionHostBindingApplies([]SolutionHostBindingDecision{
		{Document: claimant}, {Document: withdrawalOf(t, leaving, 2)},
	}, records)
	require.Equal(t, leaving.Binding, ordered[0].Document.Binding)
	require.Equal(t, claimant.Binding, ordered[1].Document.Binding)
}

func TestTheRegistryKeyIsTheAlias(t *testing.T) {
	key, err := solutionHostBindingRegistryKey(fixtureManifest(t, "reports", 1))
	require.NoError(t, err)
	require.Equal(t, "reports", key)
	_, err = solutionHostBindingRegistryKey(fixtureManifest(t, "reports.v2", 1))
	require.ErrorIs(t, err, ErrSolutionHostBindingRouteNotAddressable, "core admits a dot; a registry key does not")
}

// The pass verifies every carrier: one signed by no pinned key, or one whose
// payload is not a manifest, never reaches admission, and it is attributed to
// the binding its bytes name.
func TestThePassRefusesWhatDoesNotVerify(t *testing.T) {
	manifest := fixtureManifest(t, "reports", 1)
	other := deliverytest.Verifier(t, map[string][]string{"another-key": {testDeliveryDomain}})
	verified, problems := verifySolutionHostBindingDocuments(context.Background(), other,
		[]SolutionHostBindingDocument{{Source: "inbox:reports", Data: signedCarrier(t, manifest)}})
	require.Empty(t, verified)
	require.Len(t, problems, 1)
	require.Equal(t, manifest.Binding, problems[0].binding)
	require.ErrorIs(t, problems[0].err, solutionhost.ErrUnsigned)

	verified, problems = verifySolutionHostBindingDocuments(context.Background(), fixtureSigner(t),
		[]SolutionHostBindingDocument{{Source: "inbox:bogus", Data: carrierOver(t, []byte(`{"binding":"acme.test.bogus","schema":"codefly/module-manifest/v1"}`))}})
	require.Empty(t, verified)
	require.Len(t, problems, 1)
	require.Equal(t, "acme.test.bogus", problems[0].binding)
	require.ErrorIs(t, problems[0].err, solutionhost.ErrInvalid)
}

func TestPendingGenerationIsWhatDeliveryShowsAndTheHostHasNotApplied(t *testing.T) {
	record := appliedRecordOf(t, fixtureManifest(t, "reports", 2))
	record.Desired = &record.Applied.SolutionHostBindingGeneration
	require.Zero(t, record.PendingGeneration())
	record.Desired = &SolutionHostBindingGeneration{Generation: 3, Digest: "sha256:other"}
	require.Equal(t, uint64(3), record.PendingGeneration())
}
