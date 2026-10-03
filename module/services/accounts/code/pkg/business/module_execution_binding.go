package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Execution-bound minting: what the caller IS RUNNING, established independently
// of anything the caller says.
//
// WHAT THIS REPLACES. The mint authenticated with a shared secret, which is a
// bearer token: whoever holds it is the module, so the mint could not answer what
// the caller was running at all. A secret cannot be execution-bound in principle
// — it says "I know the secret", never "I am this pod running this image".
//
// THE THREE DIGEST TYPES, and why conflating any two defeats the check. Each
// answers a different question and they are deliberately separate types below
// rather than three strings:
//
//   - APPROVED (`ApprovedDigest`) — what the authority document says may run.
//     Signed, delivered out of band, and the only one the caller has no influence
//     over.
//   - DECLARED (`DeclaredDigest`) — what the pod's SPEC asks to run. Writable by
//     whoever can create the pod, so it is evidence of intent and never of fact.
//   - RUNNING (`RunningDigest`) — what the container's STATUS reports it is
//     actually running, as `imageID`.
//
// The check is APPROVED == RUNNING. Comparing approved against declared is the
// classic defeat: a pod spec can name an approved image and run something else
// if the tag moved, and the comparison passes while the workload is unapproved.
// Comparing declared against running proves only that the pod got what it asked
// for, which says nothing about whether it was allowed to ask.
//
// THE TAUTOLOGY THIS AVOIDS. Filling both sides of the comparison from the same
// source — reading the approved digest out of the authority document AND the
// running digest out of a field the same document provides — passes for every
// caller, because it compares a value to itself. The approved digest comes from
// the signed document; the running digest comes from the Kubernetes API, keyed by
// a pod UID that came from a TokenReview of a token the caller could not forge.
// Three independent sources, and no single one of them can satisfy the check.
//
// NOTHING IS ISSUED WHEN THE API IS UNAVAILABLE. An unreachable control plane
// means the host cannot establish what the caller is running, and a mint that
// proceeded would issue an unbound capability exactly when binding was
// impossible — the one circumstance in which the check matters most. That is not
// the same answer as a refusal, and the two are kept distinct below so a caller
// can tell "you are not approved" from "I could not tell".

// ErrExecutionUnbound reports that the host could not establish what the caller
// is running. NOT a denial: the caller may be perfectly approved.
var ErrExecutionUnbound = errors.New("cannot establish the caller's execution, so no capability may be minted")

// ErrExecutionNotApproved reports that the caller IS running something the
// authority document does not approve. A verdict.
var ErrExecutionNotApproved = errors.New("the caller is running an image the authority document does not approve")

// ErrExecutionIdentityMismatch reports that the token and the pod disagree —
// the token named a pod whose live UID differs, or whose container is absent.
var ErrExecutionIdentityMismatch = errors.New("the presented token's pod does not match the live pod")

// ErrExecutionNotBoundToken reports a token that names no pod at all.
//
// A legacy non-bound service account token authenticates the SERVICE ACCOUNT and
// nothing narrower, so it cannot be checked against a pod. Refused rather than
// skipped: skipping would make the whole check opt-out by presenting an older
// token, which is the easiest possible bypass.
var ErrExecutionNotBoundToken = errors.New("the presented token names no pod, so its execution cannot be established")

// ApprovedDigest is what the authority document approves. Signed, out of band.
type ApprovedDigest string

// RunningDigest is what a container's status reports it is running.
type RunningDigest string

// DeclaredDigest is what a pod spec asks to run.
//
// Present as a type with no comparison against it, deliberately. It exists so
// that a future reader reaching for "the pod's image" has a name for the thing
// they must NOT compare, rather than an untyped string that looks
// interchangeable with the other two.
type DeclaredDigest string

// ExecutionIdentity is what the host established about a caller, independently.
type ExecutionIdentity struct {
	PrincipalID string
	Namespace   string
	// ServiceAccount, PodName and PodUID come from the TokenReview. The UID is
	// what is compared, because a pod NAME is reused across generations of a
	// workload and the UID is not.
	ServiceAccount string
	PodName        string
	PodUID         string
	ContainerName  string
	// Running is what the container's status reports. The authority document's
	// approved digest is compared against THIS.
	Running RunningDigest
	// Incarnation is the approved build's counter, carried so a seal can name
	// it. It comes from the authority document, never from the pod: a pod
	// cannot be asked which generation of approval it belongs to.
	Incarnation uint64
}

// ExecutionReviewer is the independent source: a TokenReview, and a pod read.
//
// Two methods rather than one, because the second needs the first's answer — the
// pod is read by the name and namespace the TOKEN named, so a caller cannot
// point the host at a different pod.
type ExecutionReviewer interface {
	// ReviewToken authenticates a token and reports the identity Kubernetes
	// attributes to it. The audience is checked by the API server, so a token
	// minted for another audience is rejected there rather than here.
	ReviewToken(ctx context.Context, token, audience string) (*ReviewedExecutionToken, error)
	// RunningContainer reads what a container is running, from the pod's
	// status.
	RunningContainer(ctx context.Context, namespace, pod, container string) (*RunningContainerStatus, error)
}

// ReviewedExecutionToken is a TokenReview's answer.
type ReviewedExecutionToken struct {
	ServiceAccount string
	Namespace      string
	PodName        string
	PodUID         string
}

// RunningContainerStatus is a pod read's answer.
type RunningContainerStatus struct {
	UID     string
	ImageID string
	Found   bool
}

// ExecutionAuthority answers what a principal's approved build is.
//
// This is the authority document's half, and it takes NO attributes of the
// workload. Resolving by (service account, image digest) is exactly what lets a
// pod from a superseded generation in: it would answer "approved" for whatever
// that pod presents. The issuer answers what IT approves; the caller's running
// image is established separately; a mismatch is a refusal. Core's `SealSource`
// documents the same rule for the same reason.
type ExecutionAuthority interface {
	ApprovedBuild(ctx context.Context, principalID string) (ApprovedDigest, uint64, error)
}

// ErrNoApprovedBuild reports that a principal bears no approved build.
//
// Distinct from an unknown principal and from an unreadable authority, and the
// three-state distinction is the point:
//
//   - no approved build → the principal is known and bears none. A capability
//     may still be minted, carrying NO execution, if the caller is one that may
//     act unbound. It is not a refusal.
//   - unknown principal → refused. Nothing is known about it, so nothing can be
//     approved for it.
//   - unreadable authority → ErrExecutionUnbound. Not a verdict.
//
// Collapsing the first two is the dangerous direction: an unknown principal
// would then mint an unbound capability, which is a capability for an identity
// the host has never heard of.
var ErrNoApprovedBuild = errors.New("principal bears no approved build")

// ErrUnknownExecutionPrincipal reports a principal the authority has no record
// of at all.
var ErrUnknownExecutionPrincipal = errors.New("principal is not known to the execution authority")

// executionTokenAudience is the audience a caller's token must be minted for.
//
// A token minted for the API server's own audience would authenticate the same
// service account while having been issued for something else entirely, so the
// audience is what makes the token a credential FOR this host.
const executionTokenAudience = "accounts"

// BindExecution establishes what a caller is running and holds it against what
// the authority document approves.
//
// THE ORDER IS LOAD-BEARING, and it is: authenticate, then read the pod the
// token named, then compare. Reading the pod first would mean reading a pod the
// caller named, which is a caller-supplied input; the token is what makes the
// pod reference trustworthy.
func (s *Service) BindExecution(
	ctx context.Context, principalID, token, containerName string,
) (*ExecutionIdentity, error) {
	if s.executionReviewer == nil || s.executionAuthority == nil {
		return nil, fmt.Errorf("%w: this host has no execution reviewer wired", ErrExecutionUnbound)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: no token presented", ErrExecutionNotBoundToken)
	}
	if containerName == "" {
		// The container is what runs the image, so without it there is nothing
		// whose running digest could be read. A pod's FIRST container is the
		// tempting default and it is wrong: a sidecar would satisfy the check
		// for the workload beside it.
		return nil, fmt.Errorf("%w: no container named", ErrExecutionUnbound)
	}

	// 1. Authenticate. The API server checks the audience, so a token minted
	//    for anything else is rejected before this host reasons about it.
	reviewed, err := s.executionReviewer.ReviewToken(ctx, token, executionTokenAudience)
	if err != nil {
		// Could not reach the API server, or it refused. Both mean the host
		// cannot establish the execution; neither means the caller is
		// unapproved. The reviewer distinguishes them in its own errors and
		// both land here as unbound.
		return nil, fmt.Errorf("%w: %w", ErrExecutionUnbound, err)
	}
	if reviewed.PodUID == "" || reviewed.PodName == "" {
		return nil, fmt.Errorf("%w: the token authenticates %s/%s but names no pod",
			ErrExecutionNotBoundToken, reviewed.Namespace, reviewed.ServiceAccount)
	}

	// 2. Read the pod the TOKEN named — never one the caller named.
	running, err := s.executionReviewer.RunningContainer(
		ctx, reviewed.Namespace, reviewed.PodName, containerName)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecutionUnbound, err)
	}
	if !running.Found {
		// A missing container is NOT a non-match: the host has established
		// nothing about what is running, so it cannot say the caller is
		// unapproved.
		return nil, fmt.Errorf("%w: container %s is not in pod %s/%s",
			ErrExecutionIdentityMismatch, containerName, reviewed.Namespace, reviewed.PodName)
	}
	if running.UID != reviewed.PodUID {
		// The pod name resolved to a DIFFERENT pod than the token was issued
		// for — a replacement at the same name. This is the check the UID
		// exists for, and comparing names instead would admit it silently.
		return nil, fmt.Errorf("%w: the token names pod uid %s, live pod %s/%s is uid %s",
			ErrExecutionIdentityMismatch, reviewed.PodUID,
			reviewed.Namespace, reviewed.PodName, running.UID)
	}
	if running.ImageID == "" {
		// A container with no imageID is not yet running an identifiable image
		// (still pulling, or a runtime that does not report one). Nothing has
		// been established.
		return nil, fmt.Errorf("%w: container %s in pod %s/%s reports no image id",
			ErrExecutionUnbound, containerName, reviewed.Namespace, reviewed.PodName)
	}

	// 3. Compare against what the authority approves.
	approved, incarnation, err := s.executionAuthority.ApprovedBuild(ctx, principalID)
	switch {
	case errors.Is(err, ErrUnknownExecutionPrincipal):
		return nil, fmt.Errorf("%w: %s", ErrUnknownExecutionPrincipal, principalID)
	case errors.Is(err, ErrNoApprovedBuild):
		// Known, and bears none. The caller's execution was established; there
		// is simply nothing to hold it against. Returned WITHOUT a running
		// digest so a seal built from this carries no execution rather than an
		// unapproved one — and the pair stays whole-or-absent, which is what
		// core's optional-and-paired seal fields require.
		return &ExecutionIdentity{
			PrincipalID:    principalID,
			Namespace:      reviewed.Namespace,
			ServiceAccount: reviewed.ServiceAccount,
			PodName:        reviewed.PodName,
			PodUID:         reviewed.PodUID,
			ContainerName:  containerName,
		}, nil
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrExecutionUnbound, err)
	}

	if !digestsEqual(string(approved), running.ImageID) {
		return nil, fmt.Errorf("%w: principal %s is approved for %s and pod %s/%s is running %s",
			ErrExecutionNotApproved, principalID, approved,
			reviewed.Namespace, reviewed.PodName, running.ImageID)
	}

	return &ExecutionIdentity{
		PrincipalID:    principalID,
		Namespace:      reviewed.Namespace,
		ServiceAccount: reviewed.ServiceAccount,
		PodName:        reviewed.PodName,
		PodUID:         reviewed.PodUID,
		ContainerName:  containerName,
		Running:        RunningDigest(running.ImageID),
		Incarnation:    incarnation,
	}, nil
}

// digestsEqual compares an approved digest with a running imageID.
//
// A container's `imageID` is a REFERENCE carrying a digest, not a bare digest:
// Kubernetes reports forms like `registry/repo@sha256:…`, and some runtimes
// report `docker-pullable://registry/repo@sha256:…`. The approved digest may be
// written either as a bare `sha256:…` or as a full reference.
//
// So the comparison is on the digest PORTION, and it is an exact match on that
// portion rather than a suffix test. A suffix test is the trap: `strings.
// HasSuffix(imageID, approved)` passes when the approved digest is a suffix of a
// longer hex string, and a bare `sha256:` prefix check would let any repository
// satisfy an approval granted for one image.
//
// A value with no digest at all never matches, because an approval is only ever
// a statement about immutable content — a tag is not one.
func digestsEqual(approved, imageID string) bool {
	left, leftOK := digestPortion(approved)
	right, rightOK := digestPortion(imageID)
	return leftOK && rightOK && left == right
}

// digestPortion extracts the `sha256:<hex>` portion of a reference.
//
// Reports false when there is none, rather than returning the whole string: a
// fallback to comparing whole strings would make two tags compare equal and so
// turn a tag into an approval.
func digestPortion(reference string) (string, bool) {
	index := strings.LastIndex(reference, "@")
	candidate := reference
	if index >= 0 {
		candidate = reference[index+1:]
	}
	algorithm, hex, found := strings.Cut(candidate, ":")
	if !found || algorithm == "" || hex == "" {
		return "", false
	}
	// Only lower-case hex, and only a length a real digest has. Anything else
	// is not a digest, and accepting it would let an arbitrary string that
	// merely contains a colon pass as content-addressed.
	if len(hex) < 32 {
		return "", false
	}
	for i := 0; i < len(hex); i++ {
		c := hex[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return algorithm + ":" + hex, true
}

// SetExecutionBinding wires the independent sources.
//
// Leaving them unset means BindExecution answers ErrExecutionUnbound — never a
// mint that skips the check. A host that cannot establish what a caller is
// running must not issue a capability claiming it did.
func (s *Service) SetExecutionBinding(reviewer ExecutionReviewer, authority ExecutionAuthority) {
	s.executionReviewer = reviewer
	s.executionAuthority = authority
}
