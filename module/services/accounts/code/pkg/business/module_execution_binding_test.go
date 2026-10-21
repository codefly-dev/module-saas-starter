//go:build pure

package business

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Execution-bound minting, and every way the binding could be defeated. The
// binding is the workload: the reviewed (namespace, ServiceAccount) against the
// workloads the principal's applied manifest lists. No image is compared.

// testRunning is the declared container as a pod read reports it: running,
// regular, alone in a Running pod unless a test says otherwise.
type testRunning struct {
	UID   string
	Found bool
}

type fakeExecutionReviewer struct {
	reviewed  *ReviewedExecutionToken
	reviewErr error
	running   *testRunning
	// pod, when set, is the whole pod read answered verbatim; running is the
	// shorthand for one running regular container named container.
	pod       *PodExecution
	container string
	runErr    error
	// askedPod records which pod was read, which is how "read the pod the TOKEN
	// named" is distinguished from "read the pod the caller named".
	askedPod       string
	askedNamespace string
	askedAudience  string
}

func (f *fakeExecutionReviewer) ReviewToken(
	_ context.Context, _, audience string,
) (*ReviewedExecutionToken, error) {
	f.askedAudience = audience
	if f.reviewErr != nil {
		return nil, f.reviewErr
	}
	return f.reviewed, nil
}

func (f *fakeExecutionReviewer) ReadPod(
	_ context.Context, namespace, pod string,
) (*PodExecution, error) {
	f.askedNamespace, f.askedPod = namespace, pod
	if f.runErr != nil {
		return nil, f.runErr
	}
	if f.pod != nil {
		return f.pod, nil
	}
	read := &PodExecution{UID: f.running.UID, Phase: PodPhaseRunning}
	if f.running.Found {
		name := f.container
		if name == "" {
			name = "worker"
		}
		read.Containers = append(read.Containers, ContainerExecution{
			Name: name, Kind: ContainerRegular, State: ContainerRunning,
		})
	}
	return read, nil
}

// boundService wires the two independent sources AND declares the principal's
// workload, because the declaration is now part of the check: BindExecution
// holds the reviewed service account against the one the principal declares, so
// a service with no declaration refuses every caller as unknown.
//
// The declared workload matches reviewedPod() deliberately, so the tests below
// that are about the IMAGE are not also about the identity. The ones about the
// identity change one of these on purpose.
func boundService(reviewer ExecutionReviewer) *Service {
	return boundServiceFor(reviewer, ModuleWorkload{
		ServiceAccount: "worker", Namespace: "acme-prod", Container: "worker",
	})
}

func boundServiceFor(reviewer ExecutionReviewer, workload ModuleWorkload) *Service {
	service := &Service{store: noopControlPlaneStore{}}
	service.SetExecutionBinding(reviewer)
	service.SetModulePrincipals(ModulePrincipalRegistry{
		"principal-1": ModulePrincipalGrant{Prefix: "worker", Tenant: "11111111-1111-4111-8111-111111111111", Workloads: []ModuleWorkload{workload}},
	})
	return service
}

func reviewedPod() *ReviewedExecutionToken {
	now := time.Now().UTC()
	return &ReviewedExecutionToken{
		ServiceAccount: "worker",
		Namespace:      "acme-prod",
		PodName:        "worker-7c9f",
		PodUID:         "uid-alpha",
		IssuedAt:       now,
		ExpiresAt:      now.Add(MaxExecutionTokenLifetime),
	}
}

// ---------------------------------------------------------------------------
// The happy path, and the independence of the three sources
// ---------------------------------------------------------------------------

// A pod of a declared workload binds, and the identity carries what was
// ESTABLISHED rather than what was presented.
func TestADeclaredWorkloadBinds(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &testRunning{UID: "uid-alpha", Found: true},
	}
	service := boundService(reviewer)

	identity, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.NoError(t, err)
	require.Equal(t, "uid-alpha", identity.PodUID)
	require.Equal(t, "worker", identity.ContainerName)

	// The pod read used the namespace and name the TOKEN named. A caller cannot
	// point the host at a different pod, which is why the token comes first.
	require.Equal(t, "acme-prod", reviewer.askedNamespace)
	require.Equal(t, "worker-7c9f", reviewer.askedPod)
	require.Equal(t, "accounts", reviewer.askedAudience,
		"the token must have been minted for this host, not for the API server")
}

// ---------------------------------------------------------------------------
// Refusals: a verdict about the caller
// ---------------------------------------------------------------------------

// A replacement pod at the SAME NAME is refused, and this is the check the UID
// exists for. A name comparison would admit it silently.
func TestReplacementPodAtTheSameNameIsRefused(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		// Same name, running the approved image, DIFFERENT uid.
		running: &testRunning{UID: "uid-beta", Found: true},
	}
	service := boundService(reviewer)

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionIdentityMismatch)
	require.Contains(t, err.Error(), "uid-alpha")
	require.Contains(t, err.Error(), "uid-beta")
}

// A legacy non-bound token is REFUSED, not skipped.
//
// Skipping would make the whole check opt-out by presenting an older token,
// which is the easiest possible bypass of an execution binding.
func TestNonBoundTokenIsRefusedRatherThanSkipped(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: &ReviewedExecutionToken{ServiceAccount: "worker", Namespace: "acme-prod"},
		running:  &testRunning{UID: "uid-alpha", Found: true},
	}
	service := boundService(reviewer)

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionNotBoundToken)
	require.Empty(t, reviewer.askedPod, "no pod is read for a token that names none")
}

// An unknown principal is refused outright rather than minting an unbound
// capability — which would be a capability for an identity the host has never
// heard of.
func TestUnknownPrincipalIsRefusedNotMintedUnbound(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &testRunning{UID: "uid-alpha", Found: true},
	}
	service := boundService(reviewer)

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "stranger", "token")
	require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
}

// ---------------------------------------------------------------------------
// Unbound: the host could not tell, which is NOT a verdict
// ---------------------------------------------------------------------------

// Nothing is issued when the API is unavailable, and the answer is distinct from
// a refusal.
//
// That distinction is the whole point: an unreachable control plane means the
// host cannot establish what the caller is running, and a mint that proceeded
// would issue an unbound capability exactly when binding was impossible.
func TestNothingIsIssuedWhenTheApiIsUnavailable(t *testing.T) {
	unreachable := errors.New("dial tcp: connection refused")

	// Unreachable at the TokenReview.
	service := boundService(
		&fakeExecutionReviewer{reviewErr: unreachable})
	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnbound)

	// Unreachable at the pod read, after a successful review.
	service = boundService(
		&fakeExecutionReviewer{reviewed: reviewedPod(), runErr: unreachable})
	_, err = service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnbound)

}

// A token the API server REVIEWED and refused — minted for another audience,
// by another cluster, expired — is a verdict about the caller's credential, not
// an outage. Reading it as "unbound" told a caller presenting the wrong
// audience to retry forever, and answered 503 where the review had answered no.
func TestARejectedTokenIsAVerdictNotAnOutage(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewErr: fmt.Errorf("%w: token audiences [vault] do not include accounts", ErrExecutionTokenRejected),
	}
	service := boundService(reviewer)

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")

	require.ErrorIs(t, err, ErrExecutionTokenRejected)
	require.NotErrorIs(t, err, ErrExecutionUnbound)
	require.Equal(t, executionTokenAudience, reviewer.askedAudience,
		"the review must ask for this host's audience, so the API server refuses any other")
}

// A host with no reviewer wired cannot establish anything and says so, rather
// than minting a capability that claims it did.
func TestUnwiredHostCannotBindExecution(t *testing.T) {
	service := &Service{store: noopControlPlaneStore{}}
	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnbound)
}

// A missing container is a mismatch, not a non-match: the host established
// nothing about what is running.
func TestMissingContainerIsAMismatchNotANonMatch(t *testing.T) {
	service := boundService(
		&fakeExecutionReviewer{
			reviewed: reviewedPod(),
			running:  &testRunning{UID: "uid-alpha", Found: false},
		})

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionIdentityMismatch)
}

// No container named is refused rather than defaulting to the pod's first, and
// the property survived a change of premise.
//
// It used to assert that a caller passing an EMPTY container argument was
// refused. There is no such argument any more: the container is read from the
// declaration, which is strictly stronger — the caller cannot name one at all,
// so it cannot name a sibling either. The defaulting it guarded against is
// still the tempting mistake, because a pod's first container would satisfy the
// check for the workload beside it, so the property is kept and re-aimed at the
// declaration.
//
// The "no container declared" half is TestAPartiallyDeclaredWorkloadAuthenticatesNobody;
// this is the half that says nothing is read when the identity cannot be
// established, so a pod is never touched on the strength of a partial
// declaration.
func TestNoContainerDeclaredReadsNoPodAtAll(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &testRunning{UID: "uid-alpha", Found: true},
	}
	service := boundServiceFor(reviewer, ModuleWorkload{ServiceAccount: "worker", Namespace: "acme-prod"})

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
	require.Empty(t, reviewer.askedPod,
		"a principal whose workload is not fully declared must not cause a pod read at all")
}

// ---------------------------------------------------------------------------
// Bearing no approved build: the third state
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// The digest comparison itself, where the subtle defeats live
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// The principal is a CLAIM, and the declared workload is what checks it
// ---------------------------------------------------------------------------

// THE HOLE THIS CLOSES, which only opened when the shared secret went away.
// BindExecution takes the principal as an input, and the secret used to be what
// bound that prefix to the caller. With execution binding as the only
// authentication, a module holding its own perfectly valid service-account token
// could name ANOTHER module's prefix — and the image comparison would then test
// that other principal's approved build against this caller's running image,
// which passes whenever the two share an image. A shared base image is ordinary.
func TestACallerCannotNameAnotherPrincipalsPrefix(t *testing.T) {
	// The caller is genuinely `intruder` in the same namespace, and is running
	// the image `principal-1` is approved for — which is exactly the case that
	// passes if the service account is not checked.
	reviewer := &fakeExecutionReviewer{
		reviewed: &ReviewedExecutionToken{
			ServiceAccount: "intruder", Namespace: "acme-prod",
			PodName: "intruder-1", PodUID: "uid-intruder",
			IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
		},
		running: &testRunning{UID: "uid-intruder", Found: true},
	}
	service := boundService(reviewer)

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionIdentityMismatch,
		"a caller running the approved image must still be refused when it is not the declared workload")
	require.Contains(t, err.Error(), "intruder",
		"the refusal must name who the caller actually is, or an operator cannot act on it")
}

// The same check on the namespace half, separately, so one of the two passing
// cannot carry the other.
func TestACallerInAnotherNamespaceIsRefused(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: &ReviewedExecutionToken{
			ServiceAccount: "worker", Namespace: "acme-staging",
			PodName: "worker-7c9f", PodUID: "uid-alpha",
			IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
		},
		running: &testRunning{UID: "uid-alpha", Found: true},
	}
	service := boundService(reviewer)

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionIdentityMismatch)
	require.Contains(t, err.Error(), "acme-staging")
}

// A principal this host has no declaration for is UNKNOWN, not unapproved.
// Collapsing those is the dangerous direction: "bears no approved build" mints a
// capability carrying no execution, which for an identity the host has never
// heard of is a credential nothing can revoke.
func TestAnUndeclaredPrincipalIsUnknownRatherThanUnapproved(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &testRunning{UID: "uid-alpha", Found: true},
	}
	service := boundService(reviewer)

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-unheard-of", "token")
	require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
}

// A declaration missing any part of its workload is refused too, rather than
// checking the parts it has. A partial workload is the whole-or-absent trap: it
// reads as enforcement while leaving a term unchecked.
func TestAPartiallyDeclaredWorkloadAuthenticatesNobody(t *testing.T) {
	for name, workload := range map[string]ModuleWorkload{
		"no service account": {Namespace: "acme-prod", Container: "worker"},
		"no namespace":       {ServiceAccount: "worker", Container: "worker"},
		"no container":       {ServiceAccount: "worker", Namespace: "acme-prod"},
	} {
		t.Run(name, func(t *testing.T) {
			reviewer := &fakeExecutionReviewer{
				reviewed: reviewedPod(),
				running:  &testRunning{UID: "uid-alpha", Found: true},
			}
			service := boundServiceFor(reviewer, workload)

			_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
			require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
		})
	}
}

// THE CONTAINER COMES FROM THE DECLARATION, which is the last degree of freedom
// the caller had over its own identity. The pod holds "worker"; the principal
// declares "declared-app", which the pod does not hold — so nothing binds, and
// "worker" is not even tolerated beside it.
func TestTheContainerReadIsTheDeclaredOneNotTheCallersChoice(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &testRunning{UID: "uid-alpha", Found: true},
	}
	service := boundServiceFor(reviewer, ModuleWorkload{ServiceAccount: "worker", Namespace: "acme-prod", Container: "declared-app"})

	_, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnapprovedContainer,
		"a container the principal does not declare must not stand in for the declared one")

	reviewer.container = "declared-app"
	_, err = service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
	require.NoError(t, err)
}

// NativeDevelopment identifies this in-memory cluster fixture; it models no mesh.
func (*fakeExecutionReviewer) NativeDevelopment() bool { return true }

// A principal runs as every workload its manifest lists: the same principal
// binds from each of its services, and the container is the one declared for
// the reviewed account.
func TestEveryWorkloadOfTheManifestBindsItsPrincipal(t *testing.T) {
	service := &Service{store: noopControlPlaneStore{}}
	for _, account := range []string{"worker", "api"} {
		reviewed := reviewedPod()
		reviewed.ServiceAccount = account
		reviewer := &fakeExecutionReviewer{reviewed: reviewed, running: &testRunning{UID: "uid-alpha", Found: true}, container: account}
		service.SetExecutionBinding(reviewer)
		service.SetModulePrincipals(ModulePrincipalRegistry{"principal-1": ModulePrincipalGrant{
			Prefix: "worker", Tenant: "11111111-1111-4111-8111-111111111111", Workloads: []ModuleWorkload{
				{ServiceAccount: "worker", Namespace: "acme-prod", Container: "worker"},
				{ServiceAccount: "api", Namespace: "acme-prod", Container: "api"},
			},
		}})
		identity, err := service.BindExecution(context.Background(), service.moduleRegistrySnapshot(), "principal-1", "token")
		require.NoError(t, err, account)
		require.Equal(t, account, identity.ContainerName)
	}
}
