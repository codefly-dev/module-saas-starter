package infra_test

import (
	"context"
	"testing"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/workloadidentity"
	"github.com/codefly-dev/core/workloadidentity/native"
	"github.com/stretchr/testify/require"
)

// A native `codefly run` has no cluster: its launcher signs each process a
// credential of core's native mechanism, and the host verifies it against the
// run's anchor. These tests are the launcher's side (core's real Issuer) and
// the host's (NativeWorkloadIdentity, the key verifier's native binding).

const nativeTrustDomain = "cluster.local"

func nativeWorkload(module, service, instance string) workloadidentity.Workload {
	return workloadidentity.Workload{TrustDomain: nativeTrustDomain, Module: module, Service: service, Instance: instance}
}

// nativeRun is one run's issuer, vouching for the reports module's delivery
// carrier and api workload, and for the applied observer.
func nativeRun(t *testing.T) (*native.Issuer, *infra.NativeWorkloadIdentity) {
	t.Helper()
	issuer, err := native.NewIssuer()
	require.NoError(t, err)
	issuer.Declare(
		nativeWorkload("reports", workloadidentity.DeliveryService, ""),
		nativeWorkload("reports", "api", ""),
		nativeWorkload(workloadidentity.AppliedObserver.Module, workloadidentity.AppliedObserver.Service, ""),
	)
	attester, err := native.NewAttester(issuer.Anchor())
	require.NoError(t, err)
	return issuer, infra.NewNativeWorkloadIdentity(attester)
}

func nativeCredential(t *testing.T, issuer *native.Issuer, workload workloadidentity.Workload, container string) string {
	t.Helper()
	token, err := issuer.Issue(native.Subject{Workload: workload, Container: container}, "accounts")
	require.NoError(t, err)
	return token
}

// THE CARRIER IS THE MODULE'S DELIVERY WORKLOAD, as the launcher vouches for
// it; another workload of the module, another module's carrier, and a
// credential another run signed are each refused for what they are.
func TestANativeCarrierIsTheModulesDeliveryWorkload(t *testing.T) {
	issuer, identity := nativeRun(t)
	ctx := context.Background()
	carrier := nativeCredential(t, issuer, nativeWorkload("reports", workloadidentity.DeliveryService, "delivery-1"), "")

	require.NoError(t, identity.AuthorizeCarrier(ctx, "Bearer "+carrier, "reports"))
	require.ErrorIs(t, identity.AuthorizeCarrier(ctx, "Bearer "+carrier, "billing"), infra.ErrCarrierNotAuthorized)
	api := nativeCredential(t, issuer, nativeWorkload("reports", "api", "api-1"), "api")
	require.ErrorIs(t, identity.AuthorizeCarrier(ctx, "Bearer "+api, "reports"), infra.ErrCarrierNotAuthorized)

	other, _ := nativeRun(t)
	foreign := nativeCredential(t, other, nativeWorkload("reports", workloadidentity.DeliveryService, "delivery-1"), "")
	require.ErrorIs(t, identity.AuthorizeCarrier(ctx, "Bearer "+foreign, "reports"), infra.ErrCarrierUnauthenticated)
	require.ErrorIs(t, identity.AuthorizeCarrier(ctx, "", "reports"), infra.ErrCarrierUnauthenticated)

	observer := nativeCredential(t, issuer, nativeWorkload(workloadidentity.AppliedObserver.Module, workloadidentity.AppliedObserver.Service, "observer-1"), "")
	require.NoError(t, identity.AuthorizeAppliedObserver(ctx, "Bearer "+observer))
	require.ErrorIs(t, identity.AuthorizeAppliedObserver(ctx, "Bearer "+carrier), infra.ErrCarrierNotAuthorized,
		"the delivery carrier cannot read applied state")
}

// A MINTING PROCESS IS THE WORKLOAD THE LAUNCHER STARTED: the review names
// its module as the namespace and its service as the account, and the pod
// read that follows answers with what the launcher vouched for about that
// same instance — never another module's.
func TestANativeMintIsReviewedAsTheWorkloadTheLauncherStarted(t *testing.T) {
	issuer, identity := nativeRun(t)
	ctx := context.Background()
	token := nativeCredential(t, issuer, nativeWorkload("reports", "api", "api-1"), "api")

	reviewed, err := identity.ReviewToken(ctx, token, "accounts")
	require.NoError(t, err)
	require.Equal(t, "reports", reviewed.Namespace)
	require.Equal(t, "api", reviewed.ServiceAccount)
	require.Equal(t, "api-1", reviewed.PodName)
	require.NotEmpty(t, reviewed.PodUID)

	pod, err := identity.ReadPod(ctx, "reports", "api-1")
	require.NoError(t, err)
	require.Equal(t, reviewed.PodUID, pod.UID)
	require.Len(t, pod.Containers, 1)
	require.Equal(t, "api", pod.Containers[0].Name)

	_, err = identity.ReadPod(ctx, "billing", "api-1")
	require.ErrorIs(t, err, infra.ErrKubernetesUnavailable, "an instance is read only under the module it was attested for")

	_, err = identity.ReviewToken(ctx, token, "another-audience")
	require.ErrorIs(t, err, business.ErrExecutionTokenRejected)
	other, _ := nativeRun(t)
	_, err = identity.ReviewToken(ctx, nativeCredential(t, other, nativeWorkload("reports", "api", "api-1"), "api"), "accounts")
	require.ErrorIs(t, err, business.ErrExecutionTokenRejected)
}

// A LOCAL PROFILE BOUND TO A NATIVE RUN admits its developer only under the
// run whose anchor it pins: another run's anchor is refused at boot, and so is
// a cluster. A profile bound to a cluster is not a native one, and a profile
// may not name both.
func TestANativeLocalProfileIsProvenAgainstItsRunsAnchor(t *testing.T) {
	run, _ := nativeRun(t)
	other, _ := nativeRun(t)
	policy := func(local *infra.SolutionHostLocalProfile) *infra.SolutionHostVerificationPolicy {
		return &infra.SolutionHostVerificationPolicy{
			Local: local,
			Signers: []infra.SolutionHostSigner{{Name: "developer", Kind: solutionhost.KeyKindPerson, KeyID: "example-developer",
				PublicKey: deliverytest.PublicKey(t, "example-developer"), Domains: []string{verifierTestDomain}}},
		}
	}
	carrier := deliverytest.Carrier(t, bindingPayload(t), "example-developer")

	bound := policy(&infra.SolutionHostLocalProfile{NativeAnchorSHA256: run.Anchor().Fingerprint()})
	verifier, err := infra.NewSolutionHostKeyVerifierFrom(bound)
	require.NoError(t, err)
	_, err = verifyPresence(t, verifier, carrier)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster, "nothing is admitted before the run is proven")
	require.ErrorIs(t, verifier.ProveNativeRun(other.Anchor().Fingerprint()), infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.ErrorIs(t, verifier.ProveLocalCluster(context.Background(), newCluster(t).host(t)), infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.NoError(t, verifier.ProveNativeRun(run.Anchor().Fingerprint()))
	_, err = verifyPresence(t, verifier, carrier)
	require.NoError(t, err)

	cluster := newCluster(t)
	clusterBound, err := infra.NewSolutionHostKeyVerifierFrom(localPolicy(t, cluster.fingerprint))
	require.NoError(t, err)
	require.ErrorIs(t, clusterBound.ProveNativeRun(run.Anchor().Fingerprint()), infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	deployed, err := infra.NewSolutionHostKeyVerifierFrom(acceptedPolicy(t))
	require.NoError(t, err)
	require.ErrorIs(t, deployed.ProveNativeRun(run.Anchor().Fingerprint()), infra.ErrSolutionHostLocalPolicyOutsideItsCluster,
		"a deployed policy is never pointed at a native anchor")

	for _, local := range []*infra.SolutionHostLocalProfile{
		{NativeAnchorSHA256: run.Anchor().Fingerprint(), ClusterCertificateAuthoritySHA256: cluster.fingerprint},
		{NativeAnchorSHA256: "not-a-fingerprint"},
	} {
		_, err := infra.NewSolutionHostKeyVerifierFrom(policy(local))
		require.Error(t, err)
	}
}
