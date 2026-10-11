package business

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
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

// ErrExecutionTokenRejected reports that the API server REVIEWED the presented
// token and refused it: expired, signed by another cluster, or not minted for
// this host's audience. A verdict, not an outage — the review ran and answered —
// so it is kept apart from ErrExecutionUnbound, which is "the review could not
// run". Presenting the same token again asks the same question; the kubelet's
// next rotation is what changes the answer.
//
// The reviewer wraps it; the business layer cannot see the API server's own
// error types and must not guess from a message.
var ErrExecutionTokenRejected = errors.New("the presented token was reviewed and refused")

// ErrExecutionTokenLifetime reports a token valid for longer than
// MaxExecutionTokenLifetime, or one whose lifetime the review did not report.
// A verdict: the token is acceptable to Kubernetes and not to this host.
var ErrExecutionTokenLifetime = errors.New("the presented token outlives the 600-second projection this host requires")

// ErrExecutionWorkloadAmbiguous reports a reviewed workload (namespace and
// ServiceAccount) that this host's registry maps to more than one principal,
// or to none. A token authenticates a ServiceAccount, never a prefix, so a
// ServiceAccount shared by two principals proves neither: refused rather than
// resolved by trusting the prefix the caller asked for.
var ErrExecutionWorkloadAmbiguous = errors.New("the presented token's workload does not identify exactly one module principal")

// ErrExecutionNotRunning reports a pod or container that is not live: a pod
// being deleted or no longer Running, or an authenticating container that has
// terminated. A verdict — the host read it — and distinct from a container
// that has not started yet, which is ErrExecutionUnbound.
var ErrExecutionNotRunning = errors.New("the presented token's pod is not running its authenticating container")

// ErrExecutionUnapprovedContainer reports a pod holding a container the
// approval does not cover: a sidecar, a running or restartable init container,
// or an ephemeral (debug) container. Any of them can read the projected token,
// so the pod is one security boundary and every container in it must be
// approved.
var ErrExecutionUnapprovedContainer = errors.New("the presented token's pod runs a container the approval does not cover")

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

// DeclaredDigest is what a pod's spec asks to run.
//
// It is READ and REPORTED, never compared against an approved digest. Three
// distinct types rather than three strings is what makes the wrong comparison a
// compile error instead of a code-review question.
//
// A previous commit on this branch deleted this type as dead code, because
// nothing read a pod's spec and a type with no referent is decoration. That was
// right about the type and wrong about the gap: the fix is to read the value, not
// to drop the name. It is now populated from the pod's spec, so a refusal can
// report that a pod ASKED for the approved image and is RUNNING something else —
// which is the signature of a moved tag, and the exact defeat that comparing
// approved against declared would wave through.
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
	// Declared is what the pod's spec asked for. Carried for diagnosis only;
	// comparing the approved digest against it is the defeat this separation
	// exists to prevent, and the types make that comparison not compile.
	Declared DeclaredDigest
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
	// ReadPod reads the pod the token named: its lifecycle and EVERY container
	// in it — regular, init and ephemeral — with each one's live state, so the
	// host can refuse a pod in which anything it has not approved can run.
	ReadPod(ctx context.Context, namespace, pod string) (*PodExecution, error)
}

// ReviewedExecutionToken is a TokenReview's answer.
type ReviewedExecutionToken struct {
	ServiceAccount string
	Namespace      string
	PodName        string
	PodUID         string
	// IssuedAt and ExpiresAt are the token's own iat and exp, read from a token
	// the API server has just authenticated. They bound how long a copied
	// token stays useful, which is the one thing about replay this host can
	// hold a token to (see MaxExecutionTokenLifetime).
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// ContainerKind is where a container sits in a pod spec.
type ContainerKind string

const (
	ContainerRegular   ContainerKind = "regular"
	ContainerInit      ContainerKind = "init"
	ContainerEphemeral ContainerKind = "ephemeral"
)

// ContainerState is a container's live state as its status reports it.
type ContainerState string

const (
	ContainerRunning    ContainerState = "running"
	ContainerWaiting    ContainerState = "waiting"
	ContainerTerminated ContainerState = "terminated"
	// ContainerStateUnknown is a container the spec declares and the status
	// does not (yet) report.
	ContainerStateUnknown ContainerState = "unknown"
)

// PodExecution is a pod read's answer: what the pod is and everything in it
// that can run.
type PodExecution struct {
	UID string
	// Phase is status.phase ("Pending", "Running", "Succeeded", ...).
	Phase string
	// Terminating reports a deletionTimestamp: the pod is being torn down.
	Terminating bool
	Containers  []ContainerExecution
}

// ContainerExecution is one container of a pod, from its spec and status.
type ContainerExecution struct {
	Name  string
	Kind  ContainerKind
	State ContainerState
	// ImageID is from the container's STATUS: what it runs.
	ImageID string
	// DeclaredImage is from the pod's SPEC: what it asked to run.
	DeclaredImage string
	// Restartable is an init container with restartPolicy Always — a sidecar
	// that runs beside the application rather than before it.
	Restartable bool
}

// PodPhaseRunning is the only pod phase an execution can be bound in.
const PodPhaseRunning = "Running"

// MaxExecutionTokenLifetime is the longest-lived projected token this host
// accepts: Kubernetes' minimum `expirationSeconds`, 600. A ServiceAccount token
// is a bearer credential — whoever holds a copy can present it — so its
// lifetime is the replay window, and the host holds every token to the
// shortest the platform can issue rather than to whatever a renderer chose.
const MaxExecutionTokenLifetime = 600 * time.Second

// ExecutionAuthority answers what a principal's approved build is.
//
// This is the authority document's half, and it takes NO attributes of the
// workload. Resolving by (service account, image digest) is exactly what lets a
// pod from a superseded generation in: it would answer "approved" for whatever
// that pod presents. The issuer answers what IT approves; the caller's running
// image is established separately; a mismatch is a refusal. Core's `SealSource`
// documents the same rule for the same reason.
//
// The principal is named as the authority document names it — the module's
// prefix — never by this host's registry key.
type ExecutionAuthority interface {
	ApprovedBuild(ctx context.Context, principal string) (ApprovedDigest, uint64, error)
}

// ErrNoApprovedBuild reports that a principal bears no approved build.
//
// Distinct from an unknown principal and from an unreadable authority, and the
// three-state distinction is the point:
//
//   - no approved build → the principal is known and bears none. REFUSED.
//     Execution binding is unconditional: a capability carrying no execution is
//     one no verifier can hold against a running image, so it is not minted.
//   - unknown principal → refused. Nothing is known about it, so nothing can be
//     approved for it.
//   - unreadable authority → ErrExecutionUnbound. Not a verdict.
//
// All three refuse; the three sentinels stay separate because they send an
// operator to three different places, not because one of them admits.
//
// This comment previously said a capability "may still be minted, carrying NO
// execution" for a caller permitted to act unbound. That posture does not exist
// here, and stating it in a doc comment is itself the hazard: prose describing a
// permissive path is an instruction to build one. If a principal that may act
// unbound is ever genuinely required, it does not arrive by relaxing this —
// it arrives as a principal that is not a module, through its own surface, with
// its own test.
var ErrNoApprovedBuild = errors.New("principal bears no approved build")

// ErrUnknownExecutionPrincipal reports a principal the authority has no record
// of at all.
var ErrUnknownExecutionPrincipal = errors.New("principal is not known to the execution authority")

// ErrExecutionAuthorityUnreconciled reports that this host has an authority
// question to answer and has not yet established an answer: a ceiling is
// delivered, and no reconciliation pass has installed a view.
//
// It refuses exactly as the others do — nothing is minted — and exists because
// the alternative was to answer ErrUnknownExecutionPrincipal, which is a
// statement ABOUT THE PRINCIPAL ("never heard of it") and was being returned
// for a state that says nothing about the principal at all. An operator
// debugging a cold start was told their principal did not exist. This file
// argues at length that those two answers must not read alike; reusing one for
// the other contradicted that in the one place it is most expensive, because
// the natural response to "unknown principal" is to go and change the
// declaration that was already correct.
//
// It is NOT returned by a host that answers no authority question at all (no
// envelope, or no delivery inbox wired). That host is a correct, complete
// deployment whose every principal genuinely is unknown and always will be, so
// ErrUnknownExecutionPrincipal is the accurate answer there and activation
// already names the configuration with
// ErrSolutionAuthorityCeilingUnavailable.
var ErrExecutionAuthorityUnreconciled = errors.New(
	"the execution authority has completed no reconciliation pass")

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
	ctx context.Context, principalID, token string,
) (*ExecutionIdentity, error) {
	if s.executionReviewer == nil || s.executionAuthority == nil {
		return nil, fmt.Errorf("%w: this host has no execution reviewer wired", ErrExecutionUnbound)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: no token presented", ErrExecutionNotBoundToken)
	}

	// THE DECLARED WORKLOAD, read before the token is reviewed, because it is
	// what the review will be held against. A principal this host has no
	// declaration for is refused as unknown rather than as unapproved: nothing
	// is known about it, so nothing can be approved for it, and the two must
	// not read alike.
	grant, declared := s.declaredModules()[principalID]
	if !declared || !grant.Workload.declared() {
		return nil, fmt.Errorf("%w: %s declares no workload identity", ErrUnknownExecutionPrincipal, principalID)
	}
	// The container comes from the declaration, never from the caller. It used
	// to be an argument, which left the caller choosing which of its pod's
	// images the approval was tested against — a pod's containers do not all
	// run the same image, so naming a sibling picks the comparison.
	containerName := grant.Workload.Container
	modules := s.declaredModules()

	// 1. Authenticate. The API server checks the audience, so a token minted
	//    for anything else is rejected before this host reasons about it.
	reviewed, err := s.executionReviewer.ReviewToken(ctx, token, executionTokenAudience)
	if err != nil {
		// The review RAN and refused the token — another audience, another
		// cluster, expired. A verdict about the caller's credential, so it is
		// returned as one: reading it as "unbound" told a caller presenting a
		// token minted for the wrong audience to retry, forever.
		if errors.Is(err, ErrExecutionTokenRejected) {
			return nil, err
		}
		// The review could not run. The host cannot establish the execution,
		// and nothing about the caller has been decided.
		return nil, fmt.Errorf("%w: %w", ErrExecutionUnbound, err)
	}
	if reviewed.PodUID == "" || reviewed.PodName == "" {
		return nil, fmt.Errorf("%w: the token authenticates %s/%s but names no pod",
			ErrExecutionNotBoundToken, reviewed.Namespace, reviewed.ServiceAccount)
	}

	// THE TOKEN'S LIFETIME IS THE REPLAY WINDOW. A projected token is a bearer
	// credential: TokenReview proves the cluster issued it to this pod, never
	// that the pod is the one presenting it. The host cannot close that here
	// (see "Replay" in services/accounts/AGENTS.md); it can bound it, and it
	// bounds it to the shortest projection Kubernetes issues.
	if reviewed.IssuedAt.IsZero() || reviewed.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("%w: the review reported no issue or expiry time", ErrExecutionTokenLifetime)
	}
	if lifetime := reviewed.ExpiresAt.Sub(reviewed.IssuedAt); lifetime > MaxExecutionTokenLifetime {
		return nil, fmt.Errorf("%w: it is valid for %s", ErrExecutionTokenLifetime, lifetime)
	}

	// THE CALLER'S WORKLOAD MUST IDENTIFY EXACTLY ONE PRINCIPAL, AND IT MUST BE
	// THE ONE ASKED FOR. A token authenticates a ServiceAccount in a namespace;
	// it says nothing about a prefix. Checking only that the ASKED-FOR
	// principal declares this workload let two principals sharing a
	// ServiceAccount (and an approved image) each be minted from either one's
	// token: alpha's own pod asked for prefix=beta and got beta's tenant. So
	// the workload is resolved to its principal over the WHOLE registry, and
	// anything but exactly one owner is refused — never resolved by trusting
	// the prefix the caller asked for. A verdict, not "unbound": the host
	// established exactly who the caller is.
	owners := principalsDeclaringWorkload(modules, reviewed.Namespace, reviewed.ServiceAccount)
	switch {
	case len(owners) > 1:
		return nil, fmt.Errorf("%w: %s/%s is declared by %d principals (%s)",
			ErrExecutionWorkloadAmbiguous, reviewed.Namespace, reviewed.ServiceAccount,
			len(owners), strings.Join(owners, ", "))
	case len(owners) == 0 || owners[0] != principalID:
		return nil, fmt.Errorf(
			"%w: %s is declared to run as %s/%s, the presented token authenticates %s/%s",
			ErrExecutionIdentityMismatch, principalID,
			grant.Workload.Namespace, grant.Workload.ServiceAccount,
			reviewed.Namespace, reviewed.ServiceAccount)
	}

	// 2. Read the pod the TOKEN named — never one the caller named.
	pod, err := s.executionReviewer.ReadPod(ctx, reviewed.Namespace, reviewed.PodName)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecutionUnbound, err)
	}
	if pod.UID != reviewed.PodUID {
		// The pod name resolved to a DIFFERENT pod than the token was issued
		// for — a replacement at the same name. This is the check the UID
		// exists for, and comparing names instead would admit it silently.
		return nil, fmt.Errorf("%w: the token names pod uid %s, live pod %s/%s is uid %s",
			ErrExecutionIdentityMismatch, reviewed.PodUID,
			reviewed.Namespace, reviewed.PodName, pod.UID)
	}
	app, err := authenticatingContainer(pod, containerName, reviewed)
	if err != nil {
		return nil, err
	}
	// 3. Compare against what the authority approves.
	//
	// The approval is looked up by the name the AUTHORITY DOCUMENT gives the
	// principal, which is the module's prefix (core's
	// PrincipalAuthority.Principal), not by this host's registry key: the
	// registry keys a principal by ModulePrincipalID(prefix), a UUID no
	// document carries. Asking the approved-build view for the UUID answered
	// "unknown principal" for every module the moment the mint first called
	// this, which is how the two keys were found to differ (#1056).
	approved, incarnation, err := s.executionAuthority.ApprovedBuild(ctx, grant.Prefix)
	switch {
	case errors.Is(err, ErrUnknownExecutionPrincipal):
		return nil, fmt.Errorf("%w: %s", ErrUnknownExecutionPrincipal, principalID)
	case errors.Is(err, ErrNoApprovedBuild):
		// Known, and bears none: REFUSED. Execution binding is unconditional,
		// so there is no such thing as a module capability carrying no
		// execution.
		//
		// This branch used to return an identity with no running digest, for "a
		// caller that may act unbound". Nothing could reach it — the only
		// production ExecutionAuthority never calls Declare(), so the view
		// answers a record or ErrUnknownExecutionPrincipal and never this. It
		// was a fully built permissive path waiting for a trigger: adding the
		// one Declare() call that the next legitimate requirement (a human
		// session, say) obviously wants would have converted this refusal into
		// a mintable capability, for a principal whose running build nothing
		// checked, with no test failing. Deleting it is the point, not tidiness.
		//
		// The sentinel stays distinct from ErrUnknownExecutionPrincipal because
		// the two are different operator problems — "declared but not yet
		// approved" against "never heard of it" — and collapsing them would
		// send the operator to the wrong place. Both refuse.
		return nil, fmt.Errorf("%w: %s", ErrNoApprovedBuild, principalID)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrExecutionUnbound, err)
	}

	if !digestsEqual(string(approved), app.ImageID) {
		// The declared image is named in the refusal precisely because the
		// interesting case is when it MATCHES the approval and the running image
		// does not: that is a moved tag, and an operator reading only "not
		// approved" would go looking at the authority document instead of the
		// registry.
		return nil, fmt.Errorf(
			"%w: principal %s is approved for %s, pod %s/%s asked for %s and is running %s",
			ErrExecutionNotApproved, principalID, approved,
			reviewed.Namespace, reviewed.PodName, app.DeclaredImage, app.ImageID)
	}

	return &ExecutionIdentity{
		PrincipalID:    principalID,
		Namespace:      reviewed.Namespace,
		ServiceAccount: reviewed.ServiceAccount,
		PodName:        reviewed.PodName,
		PodUID:         reviewed.PodUID,
		ContainerName:  containerName,
		Running:        RunningDigest(app.ImageID),
		Declared:       DeclaredDigest(app.DeclaredImage),
		Incarnation:    incarnation,
	}, nil
}

// principalsDeclaringWorkload returns, sorted, every principal whose declared
// workload runs as serviceAccount in namespace. The container is deliberately
// not part of the key: a token authenticates the ServiceAccount, and every
// container in a pod can read it.
func principalsDeclaringWorkload(modules ModulePrincipalRegistry, namespace, serviceAccount string) []string {
	var owners []string
	for id, grant := range modules {
		if grant.Workload.Namespace == namespace && grant.Workload.ServiceAccount == serviceAccount {
			owners = append(owners, id)
		}
	}
	slices.Sort(owners)
	return owners
}

// authenticatingContainer holds a pod read to what an execution-bound mint
// requires, and returns the container whose running image the approval is
// compared against.
//
// THE POD IS ONE SECURITY BOUNDARY. Every container in it can read the
// projected token, so approving one container's image while another runs
// beside it approves the other too. The approval covers exactly one container
// — the one the principal declares — so:
//
//   - the pod must be Running and not being deleted;
//   - the authenticating container must be a REGULAR container in state
//     Running; an init container never authenticates, and a completed one
//     never supplies the digest;
//   - no other regular container, no init container that is still running or
//     restartable (a sidecar), and no ephemeral container in any state may be
//     present. Init containers that ran to completion before the application
//     started are the only others tolerated.
//
// WHY NOT ADMIT A DECLARED SIDECAR AT ITS DIGEST. That is the stronger-and-
// workable rule — every container the applied, signed presence document
// declares, each running its declared digest — and it cannot be enforced yet,
// because presence does not carry the digests. core's solutionhost.Workload
// (v0.17.2 through main) names the authenticating Container with its Image
// digest, and the others only as NonAuthenticating NAMES; the CLI renders
// exactly that (cli pkg/gitops/presence_workloads.go). Admitting a sidecar by
// name would let it run any image, so every other container is refused instead.
// The cost: a module pod with a sidecar — a Cloud SQL Auth Proxy, an
// istio-injected namespace — mints nothing until presence carries a digest per
// container (a core field, the CLI's render, and this check reading it).
//
// A pod that has not started its container yet is ErrExecutionUnbound — the
// host could not establish a live execution, and a retry may — while a pod or
// container that has stopped or is being deleted is a verdict.
func authenticatingContainer(pod *PodExecution, name string, reviewed *ReviewedExecutionToken) (*ContainerExecution, error) {
	where := reviewed.Namespace + "/" + reviewed.PodName
	if pod.Terminating {
		return nil, fmt.Errorf("%w: pod %s is being deleted", ErrExecutionNotRunning, where)
	}
	switch pod.Phase {
	case PodPhaseRunning:
	case "Pending", "":
		return nil, fmt.Errorf("%w: pod %s is %q, not yet Running", ErrExecutionUnbound, where, pod.Phase)
	default:
		return nil, fmt.Errorf("%w: pod %s is %s", ErrExecutionNotRunning, where, pod.Phase)
	}
	var app *ContainerExecution
	var unapproved []string
	for i := range pod.Containers {
		container := &pod.Containers[i]
		switch {
		case container.Kind == ContainerRegular && container.Name == name:
			app = container
		case container.Kind == ContainerInit && !container.Restartable && container.State == ContainerTerminated:
			// Ran to completion before the application started. Tolerated,
			// and never a source of the digest.
		default:
			unapproved = append(unapproved, string(container.Kind)+" "+container.Name)
		}
	}
	if len(unapproved) > 0 {
		return nil, fmt.Errorf("%w: pod %s also holds %s, which the approval for container %s does not cover",
			ErrExecutionUnapprovedContainer, where, strings.Join(unapproved, ", "), name)
	}
	if app == nil {
		// A missing container is NOT a non-match: the host has established
		// nothing about what is running, so it cannot say the caller is
		// unapproved.
		return nil, fmt.Errorf("%w: container %s is not a container of pod %s",
			ErrExecutionIdentityMismatch, name, where)
	}
	switch app.State {
	case ContainerRunning:
	case ContainerTerminated:
		return nil, fmt.Errorf("%w: container %s in pod %s has terminated", ErrExecutionNotRunning, name, where)
	default:
		return nil, fmt.Errorf("%w: container %s in pod %s is %s, not running", ErrExecutionUnbound, name, where, app.State)
	}
	if app.ImageID == "" {
		// A container with no imageID is not running an identifiable image
		// (a runtime that does not report one). Nothing has been established.
		return nil, fmt.Errorf("%w: container %s in pod %s reports no image id", ErrExecutionUnbound, name, where)
	}
	return app, nil
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
