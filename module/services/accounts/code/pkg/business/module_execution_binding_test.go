//go:build pure

package business

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Execution-bound minting, and every way the binding could be defeated.
//
// The digests here are realistic shapes rather than short strings, because the
// comparison's correctness depends on the shapes: a container's `imageID` is a
// digest-bearing REFERENCE, not a bare digest, and that is where a suffix test
// or a prefix check goes wrong.

const (
	approvedRef = "registry.example.com/acme/worker@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
	// The same digest as a bare value, which is the other legitimate spelling.
	approvedBare = "sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
	// A different image, same registry and repository.
	otherRef = "registry.example.com/acme/worker@sha256:" +
		"2222222222222222222222222222222222222222222222222222222222222222"
	// The approved digest as some runtimes report it.
	pullableRef = "docker-pullable://registry.example.com/acme/worker@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
)

type fakeExecutionReviewer struct {
	reviewed  *ReviewedExecutionToken
	reviewErr error
	running   *RunningContainerStatus
	runErr    error
	// askedPod records which pod was read, which is how "read the pod the TOKEN
	// named" is distinguished from "read the pod the caller named".
	askedPod       string
	askedNamespace string
	askedContainer string
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

func (f *fakeExecutionReviewer) RunningContainer(
	_ context.Context, namespace, pod, container string,
) (*RunningContainerStatus, error) {
	f.askedNamespace, f.askedPod, f.askedContainer = namespace, pod, container
	if f.runErr != nil {
		return nil, f.runErr
	}
	return f.running, nil
}

type fakeExecutionAuthority struct {
	digest      ApprovedDigest
	incarnation uint64
	err         error
}

func (f *fakeExecutionAuthority) ApprovedBuild(
	_ context.Context, _ string,
) (ApprovedDigest, uint64, error) {
	if f.err != nil {
		return "", 0, f.err
	}
	return f.digest, f.incarnation, nil
}

// boundService wires the two independent sources AND declares the principal's
// workload, because the declaration is now part of the check: BindExecution
// holds the reviewed service account against the one the principal declares, so
// a service with no declaration refuses every caller as unknown.
//
// The declared workload matches reviewedPod() deliberately, so the tests below
// that are about the IMAGE are not also about the identity. The ones about the
// identity change one of these on purpose.
func boundService(reviewer ExecutionReviewer, authority ExecutionAuthority) *Service {
	return boundServiceFor(reviewer, authority, ModuleWorkload{
		ServiceAccount: "worker", Namespace: "acme-prod", Container: "worker",
	})
}

func boundServiceFor(
	reviewer ExecutionReviewer, authority ExecutionAuthority, workload ModuleWorkload,
) *Service {
	service := &Service{store: noopControlPlaneStore{}}
	service.SetExecutionBinding(reviewer, authority)
	service.SetModulePrincipals(ModulePrincipalRegistry{
		"principal-1": ModulePrincipalGrant{Prefix: "worker", Tenant: "11111111-1111-4111-8111-111111111111", Workload: workload},
	})
	return service
}

func reviewedPod() *ReviewedExecutionToken {
	return &ReviewedExecutionToken{
		ServiceAccount: "worker",
		Namespace:      "acme-prod",
		PodName:        "worker-7c9f",
		PodUID:         "uid-alpha",
	}
}

// ---------------------------------------------------------------------------
// The happy path, and the independence of the three sources
// ---------------------------------------------------------------------------

// A pod running the approved image binds, and the identity carries what was
// ESTABLISHED rather than what was presented.
func TestApprovedRunningImageBinds(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 7})

	identity, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.NoError(t, err)
	require.Equal(t, RunningDigest(approvedRef), identity.Running)
	require.Equal(t, uint64(7), identity.Incarnation,
		"the incarnation comes from the authority document, never from the pod")
	require.Equal(t, "uid-alpha", identity.PodUID)

	// The pod read used the namespace and name the TOKEN named. A caller cannot
	// point the host at a different pod, which is why the token comes first.
	require.Equal(t, "acme-prod", reviewer.askedNamespace)
	require.Equal(t, "worker-7c9f", reviewer.askedPod)
	require.Equal(t, "accounts", reviewer.askedAudience,
		"the token must have been minted for this host, not for the API server")
}

// Approved written as a full reference and running reported as a pullable URI
// still bind: the comparison is on the digest portion, not on the spelling.
func TestDigestComparisonIsOnTheDigestNotTheSpelling(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: pullableRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedRef, incarnation: 1})

	identity, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.NoError(t, err)
	require.Equal(t, uint64(1), identity.Incarnation)
}

// ---------------------------------------------------------------------------
// Refusals: a verdict about the caller
// ---------------------------------------------------------------------------

// THE MOVED TAG, and the case that proves the three digest types are distinct by
// execution rather than by comment.
//
// The pod's SPEC names exactly the approved image. Its STATUS reports something
// else — which is what a moved tag produces, and what a mutable registry
// reference makes possible at any time. Comparing approved against DECLARED
// would pass here and admit an unapproved workload; comparing declared against
// running would prove only that the pod got what it asked for.
//
// Approved == RUNNING is the only comparison that refuses it.
func TestAPodThatAskedForTheApprovedImageAndRunsAnotherIsRefused(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running: &RunningContainerStatus{
			UID: "uid-alpha",
			// Asked for the approved image...
			DeclaredImage: approvedRef,
			// ...and is running a different one.
			ImageID: otherRef,
			Found:   true,
		},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 1})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionNotApproved)
	// The refusal names BOTH, because an operator told only "not approved" would
	// go reading the authority document when the registry is what moved.
	require.Contains(t, err.Error(), approvedRef, "the refusal must name what was asked for")
	require.Contains(t, err.Error(), otherRef, "and what is actually running")
}

// And the control: the same spec with a RUNNING image that matches is admitted,
// so the test above is not passing because every declared image is refused.
func TestAPodRunningWhatItAskedForAndWhatIsApprovedIsAdmitted(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running: &RunningContainerStatus{
			UID: "uid-alpha", DeclaredImage: approvedRef, ImageID: approvedRef, Found: true,
		},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 4})

	identity, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.NoError(t, err)
	require.Equal(t, RunningDigest(approvedRef), identity.Running)
	require.Equal(t, DeclaredDigest(approvedRef), identity.Declared,
		"the declared image is carried for diagnosis, distinct from the running one")
}

// A pod running a DIFFERENT image from the same repository is refused. Same
// registry, same repo, different digest — which is what a moved tag produces.
func TestUnapprovedRunningImageIsRefused(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: otherRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 1})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionNotApproved)
	require.NotErrorIs(t, err, ErrExecutionUnbound,
		"this is a verdict about the caller, not a failure to establish one")
}

// A replacement pod at the SAME NAME is refused, and this is the check the UID
// exists for. A name comparison would admit it silently.
func TestReplacementPodAtTheSameNameIsRefused(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		// Same name, running the approved image, DIFFERENT uid.
		running: &RunningContainerStatus{UID: "uid-beta", ImageID: approvedRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 1})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
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
		running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionNotBoundToken)
	require.Empty(t, reviewer.askedPod, "no pod is read for a token that names none")
}

// An unknown principal is refused outright rather than minting an unbound
// capability — which would be a capability for an identity the host has never
// heard of.
func TestUnknownPrincipalIsRefusedNotMintedUnbound(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{err: ErrUnknownExecutionPrincipal})

	_, err := service.BindExecution(context.Background(), "stranger", "token")
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
		&fakeExecutionReviewer{reviewErr: unreachable},
		&fakeExecutionAuthority{digest: approvedBare})
	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnbound)
	require.NotErrorIs(t, err, ErrExecutionNotApproved)

	// Unreachable at the pod read, after a successful review.
	service = boundService(
		&fakeExecutionReviewer{reviewed: reviewedPod(), runErr: unreachable},
		&fakeExecutionAuthority{digest: approvedBare})
	_, err = service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnbound)

	// Unreadable authority document.
	service = boundService(
		&fakeExecutionReviewer{
			reviewed: reviewedPod(),
			running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
		},
		&fakeExecutionAuthority{err: unreachable})
	_, err = service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnbound)
	require.NotErrorIs(t, err, ErrExecutionNotApproved)
}

// A host with no reviewer wired cannot establish anything and says so, rather
// than minting a capability that claims it did.
func TestUnwiredHostCannotBindExecution(t *testing.T) {
	service := &Service{store: noopControlPlaneStore{}}
	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnbound)
}

// A container still pulling reports no imageID: nothing has been established, so
// it is unbound rather than unapproved.
func TestContainerWithNoImageIdIsUnboundNotUnapproved(t *testing.T) {
	service := boundService(
		&fakeExecutionReviewer{
			reviewed: reviewedPod(),
			running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: "", Found: true},
		},
		&fakeExecutionAuthority{digest: approvedBare})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionUnbound)
	require.NotErrorIs(t, err, ErrExecutionNotApproved)
}

// A missing container is a mismatch, not a non-match: the host established
// nothing about what is running.
func TestMissingContainerIsAMismatchNotANonMatch(t *testing.T) {
	service := boundService(
		&fakeExecutionReviewer{
			reviewed: reviewedPod(),
			running:  &RunningContainerStatus{UID: "uid-alpha", Found: false},
		},
		&fakeExecutionAuthority{digest: approvedBare})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrExecutionIdentityMismatch)
	require.NotErrorIs(t, err, ErrExecutionNotApproved)
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
		running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
	}
	service := boundServiceFor(reviewer, &fakeExecutionAuthority{digest: approvedBare},
		ModuleWorkload{ServiceAccount: "worker", Namespace: "acme-prod"})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
	require.Empty(t, reviewer.askedPod,
		"a principal whose workload is not fully declared must not cause a pod read at all")
}

// ---------------------------------------------------------------------------
// Bearing no approved build: the third state
// ---------------------------------------------------------------------------

// A known principal bearing no approved build binds WITHOUT an execution, and
// the pair stays whole-or-absent.
//
// Both fields absent together is what core's optional-and-paired seal requires:
// a digest with no incarnation, or an incarnation with no digest, is refused by
// its schema.
func TestPrincipalBearingNoApprovedBuildBindsWithoutAnExecution(t *testing.T) {
	service := boundService(
		&fakeExecutionReviewer{
			reviewed: reviewedPod(),
			running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
		},
		&fakeExecutionAuthority{err: ErrNoApprovedBuild})

	identity, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.NoError(t, err, "bearing no approved build is not a refusal")
	require.Empty(t, identity.Running, "no execution is claimed")
	require.Zero(t, identity.Incarnation,
		"the pair is whole-or-absent: an incarnation without a digest would be refused by core's schema")
	// The identity was still established, which is what makes this different
	// from an unbound answer.
	require.Equal(t, "uid-alpha", identity.PodUID)
}

// ---------------------------------------------------------------------------
// The digest comparison itself, where the subtle defeats live
// ---------------------------------------------------------------------------

// A tag is never an approval, however it is spelled.
//
// An approval is a statement about immutable content, and a tag is not one, so a
// value with no digest must never match — including when both sides carry the
// same tag, which a whole-string fallback would accept.
func TestATagNeverMatches(t *testing.T) {
	for _, pair := range []struct{ approved, running string }{
		{"registry.example.com/acme/worker:v1", "registry.example.com/acme/worker:v1"},
		{approvedBare, "registry.example.com/acme/worker:v1"},
		{"registry.example.com/acme/worker:v1", approvedRef},
		{"", approvedRef},
		{approvedBare, ""},
	} {
		require.False(t, digestsEqual(pair.approved, pair.running),
			"approved %q must not match running %q", pair.approved, pair.running)
	}
}

// A suffix is not a match, which is the specific trap `strings.HasSuffix` sets:
// an approved digest that is a suffix of a longer hex string would pass.
func TestASuffixIsNotAMatch(t *testing.T) {
	longer := "sha256:ff" + strings.TrimPrefix(approvedBare, "sha256:")
	require.False(t, digestsEqual(approvedBare, "registry.example.com/acme/worker@"+longer),
		"a digest that merely ENDS WITH the approved one is a different image")
}

// A shared prefix is not a match either: checking only that both start with
// `sha256:` would let any repository satisfy an approval granted for one image.
func TestASharedAlgorithmPrefixIsNotAMatch(t *testing.T) {
	require.False(t, digestsEqual(approvedBare, otherRef))
}

// A non-hex or too-short value is not a digest, so it never matches — otherwise
// any string containing a colon would pass as content-addressed.
func TestANonDigestIsNotADigest(t *testing.T) {
	for _, value := range []string{"sha256:short", "sha256:" + strings.Repeat("z", 64), "notadigest", "sha256:"} {
		_, ok := digestPortion(value)
		require.False(t, ok, "%q must not read as a digest", value)
	}
	// And the control: the real shapes DO read as digests, or the test above
	// would pass against a function that rejects everything.
	for _, value := range []string{approvedBare, approvedRef, pullableRef} {
		_, ok := digestPortion(value)
		require.True(t, ok, "%q must read as a digest", value)
	}
}

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
		},
		running: &RunningContainerStatus{UID: "uid-intruder", ImageID: approvedRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 7})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
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
		},
		running: &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 7})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
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
		running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
	}
	service := boundService(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 7})

	_, err := service.BindExecution(context.Background(), "principal-unheard-of", "token")
	require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
	require.NotErrorIs(t, err, ErrNoApprovedBuild,
		"an unknown principal must not read as one that bears no build, which would mint an unbound capability")
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
				running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
			}
			service := boundServiceFor(reviewer,
				&fakeExecutionAuthority{digest: approvedBare, incarnation: 7}, workload)

			_, err := service.BindExecution(context.Background(), "principal-1", "token")
			require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
		})
	}
}

// THE CONTAINER COMES FROM THE DECLARATION, which is the last degree of freedom
// the caller had over its own identity: a pod's containers do not all run the
// same image, so naming a sibling picks which image the approval is tested
// against.
func TestTheContainerReadIsTheDeclaredOneNotTheCallersChoice(t *testing.T) {
	reviewer := &fakeExecutionReviewer{
		reviewed: reviewedPod(),
		running:  &RunningContainerStatus{UID: "uid-alpha", ImageID: approvedRef, Found: true},
	}
	service := boundServiceFor(reviewer,
		&fakeExecutionAuthority{digest: approvedBare, incarnation: 7},
		ModuleWorkload{ServiceAccount: "worker", Namespace: "acme-prod", Container: "declared-app"})

	_, err := service.BindExecution(context.Background(), "principal-1", "token")
	require.NoError(t, err)
	require.Equal(t, "declared-app", reviewer.askedContainer,
		"the pod read must ask for the DECLARED container; reading any other picks which image the approval is tested against")
}
