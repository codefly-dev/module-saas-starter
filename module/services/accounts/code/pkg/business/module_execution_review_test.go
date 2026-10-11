//go:build pure

package business

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Regressions for review round 1 on #1056. Each was reproduced by a probe that
// MINTED; each is a refusal here.

// runningPodWith is a Running pod whose declared "worker" container runs the
// approved image, plus whatever else a test puts beside it.
func runningPodWith(others ...ContainerExecution) *PodExecution {
	return &PodExecution{UID: "uid-alpha", Phase: PodPhaseRunning, Containers: append([]ContainerExecution{{
		Name: "worker", Kind: ContainerRegular, State: ContainerRunning, ImageID: approvedRef, DeclaredImage: approvedRef,
	}}, others...)}
}

func bindAgainst(t *testing.T, pod *PodExecution) error {
	t.Helper()
	service := boundService(&fakeExecutionReviewer{reviewed: reviewedPod(), pod: pod},
		&fakeExecutionAuthority{digest: approvedBare, incarnation: 3})
	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	return err
}

// FINDING 1. Two principals declaring the same ServiceAccount, namespace,
// container and approved image: one pod's own token minted as EITHER prefix.
// A token authenticates a ServiceAccount, never a prefix, so a shared
// ServiceAccount proves neither — both are refused.
func TestAWorkloadSharedByTwoPrincipalsMintsAsNeither(t *testing.T) {
	shared := ModuleWorkload{ServiceAccount: "worker", Namespace: "acme-prod", Container: "worker"}
	reviewer := &fakeExecutionReviewer{reviewed: reviewedPod(), running: &testRunning{UID: "uid-alpha", ImageID: approvedRef, Found: true}}
	service := &Service{store: noopControlPlaneStore{}}
	service.SetExecutionBinding(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 1})
	service.SetModulePrincipals(ModulePrincipalRegistry{
		"principal-alpha": {Prefix: "alpha", Tenant: "11111111-1111-4111-8111-111111111111", Workload: shared},
		"principal-beta":  {Prefix: "beta", Tenant: "22222222-2222-4222-8222-222222222222", Workload: shared},
	})
	for _, principal := range []string{"principal-alpha", "principal-beta"} {
		_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), principal, "token")
		require.ErrorIs(t, err, ErrExecutionWorkloadAmbiguous, principal)
		require.Contains(t, err.Error(), "principal-alpha")
		require.Contains(t, err.Error(), "principal-beta")
	}

	// The control: given distinct ServiceAccounts, alpha's token mints alpha
	// and never beta.
	service.SetModulePrincipals(ModulePrincipalRegistry{
		"principal-alpha": {Prefix: "alpha", Tenant: "11111111-1111-4111-8111-111111111111", Workload: shared},
		"principal-beta": {Prefix: "beta", Tenant: "22222222-2222-4222-8222-222222222222",
			Workload: ModuleWorkload{ServiceAccount: "beta", Namespace: "acme-prod", Container: "worker"}},
	})
	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-alpha", "token")
	require.NoError(t, err)
	_, err = service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-beta", "token")
	require.ErrorIs(t, err, ErrExecutionIdentityMismatch)
}

// FINDING 2a. The pod is one security boundary: every container in it can read
// the projected token. Anything beside the authenticating container that the
// approval does not cover refuses the mint — whatever state it is in, for an
// ephemeral (debug) container, a sidecar or a restartable init container.
func TestAnyUnapprovedContainerInThePodMintsNothing(t *testing.T) {
	other := "registry.example/sidecar@sha256:" + "3333333333333333333333333333333333333333333333333333333333333333"
	for name, container := range map[string]ContainerExecution{
		"a running sidecar":                        {Name: "sidecar", Kind: ContainerRegular, State: ContainerRunning, ImageID: other},
		"a stopped sidecar that can restart":       {Name: "sidecar", Kind: ContainerRegular, State: ContainerTerminated, ImageID: other},
		"a sidecar not yet reported":               {Name: "sidecar", Kind: ContainerRegular, State: ContainerStateUnknown},
		"a running ephemeral container":            {Name: "debug", Kind: ContainerEphemeral, State: ContainerRunning, ImageID: other},
		"an exited ephemeral container":            {Name: "debug", Kind: ContainerEphemeral, State: ContainerTerminated, ImageID: other},
		"an init container still running":          {Name: "setup", Kind: ContainerInit, State: ContainerRunning, ImageID: other},
		"a restartable init (sidecar)":             {Name: "proxy", Kind: ContainerInit, State: ContainerTerminated, ImageID: other, Restartable: true},
		"a second container on the approved image": {Name: "twin", Kind: ContainerRegular, State: ContainerRunning, ImageID: approvedRef},
	} {
		t.Run(name, func(t *testing.T) {
			err := bindAgainst(t, runningPodWith(container))
			require.ErrorIs(t, err, ErrExecutionUnapprovedContainer)
			require.Contains(t, err.Error(), container.Name)
		})
	}

	// The control: an init container that ran to completion before the
	// application started is tolerated, and never a source of the digest.
	require.NoError(t, bindAgainst(t, runningPodWith(
		ContainerExecution{Name: "migrate", Kind: ContainerInit, State: ContainerTerminated, ImageID: approvedRef})))
}

// FINDING 3. The authenticating container must be a REGULAR container that is
// RUNNING, in a Running pod that is not being deleted. A stopped container's
// status keeps its approved imageID; a completed init container named like the
// declared one still reports one. Neither supplies the digest.
func TestOnlyALiveRegularContainerSuppliesTheDigest(t *testing.T) {
	stopped := runningPodWith()
	stopped.Containers[0].State = ContainerTerminated
	require.ErrorIs(t, bindAgainst(t, stopped), ErrExecutionNotRunning, "a terminated app")

	initOnly := &PodExecution{UID: "uid-alpha", Phase: PodPhaseRunning, Containers: []ContainerExecution{{
		Name: "worker", Kind: ContainerInit, State: ContainerTerminated, ImageID: approvedRef,
	}}}
	require.ErrorIs(t, bindAgainst(t, initOnly), ErrExecutionIdentityMismatch, "a completed init container named like the app")

	deleting := runningPodWith()
	deleting.Terminating = true
	require.ErrorIs(t, bindAgainst(t, deleting), ErrExecutionNotRunning, "a pod being deleted")

	for _, phase := range []string{"Succeeded", "Failed", "Unknown"} {
		finished := runningPodWith()
		finished.Phase = phase
		require.ErrorIs(t, bindAgainst(t, finished), ErrExecutionNotRunning, phase)
	}

	// Not started yet is the host not knowing, not a verdict: retryable.
	pending := runningPodWith()
	pending.Phase = "Pending"
	require.ErrorIs(t, bindAgainst(t, pending), ErrExecutionUnbound)
	waiting := runningPodWith()
	waiting.Containers[0].State = ContainerWaiting
	require.ErrorIs(t, bindAgainst(t, waiting), ErrExecutionUnbound)
}

// FINDING 3, the race: the token is reviewed while the container runs, and the
// container stops before the pod is read — same pod, same UID, same imageID.
// The UID check cannot see it; the state check does.
func TestAContainerThatStopsBetweenReviewAndReadMintsNothing(t *testing.T) {
	pod := runningPodWith()
	reviewer := &stoppingReviewer{fakeExecutionReviewer: fakeExecutionReviewer{reviewed: reviewedPod(), pod: pod}}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 3})

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")

	require.ErrorIs(t, err, ErrExecutionNotRunning)
	require.Equal(t, "uid-alpha", pod.UID, "the UID did not change, which is why the UID check alone admitted it")
	require.Equal(t, approvedRef, pod.Containers[0].ImageID)
}

type stoppingReviewer struct{ fakeExecutionReviewer }

func (r *stoppingReviewer) ReviewToken(ctx context.Context, token, audience string) (*ReviewedExecutionToken, error) {
	reviewed, err := r.fakeExecutionReviewer.ReviewToken(ctx, token, audience)
	r.pod.Containers[0].State = ContainerTerminated
	return reviewed, err
}

// FINDING 2b. A projected token is a bearer credential, so its lifetime is the
// replay window. The host accepts none longer than Kubernetes' minimum
// projection, and none whose lifetime the review did not report.
func TestATokenLongerLivedThanTheMinimumProjectionMintsNothing(t *testing.T) {
	bind := func(reviewed *ReviewedExecutionToken) error {
		service := boundService(&fakeExecutionReviewer{reviewed: reviewed, pod: runningPodWith()},
			&fakeExecutionAuthority{digest: approvedBare, incarnation: 1})
		_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
		return err
	}
	hour := reviewedPod()
	hour.ExpiresAt = hour.IssuedAt.Add(time.Hour)
	require.ErrorIs(t, bind(hour), ErrExecutionTokenLifetime, "the 3600s default projection")

	unreported := reviewedPod()
	unreported.IssuedAt = time.Time{}
	require.ErrorIs(t, bind(unreported), ErrExecutionTokenLifetime)

	exact := reviewedPod()
	exact.ExpiresAt = exact.IssuedAt.Add(MaxExecutionTokenLifetime)
	require.NoError(t, bind(exact))
}
