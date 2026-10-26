package business

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Execution-bound minting: WHICH WORKLOAD the caller is, established
// independently of anything the caller says.
//
// A module proves which module it is with its pod's projected ServiceAccount
// token. The host reviews it (TokenReview), which names the namespace, the
// ServiceAccount and the pod; reads that pod; and holds the pair to the
// workloads the module's APPLIED manifest lists, spelled by the cell's rule
// (namespace from the module, ServiceAccount from the service). No image is
// compared: build approval left the runtime path with the module manifest
// (core: "an authority approves the build of every workload it runs" is
// deploy-time admission), and a Work Context carries no build seal.
//
// NOTHING IS ISSUED WHEN THE API IS UNAVAILABLE. An unreachable control plane
// means the host cannot establish which workload is calling, and that is kept
// distinct from a refusal so a caller can tell "you are not this module" from
// "I could not tell".

// ErrExecutionUnbound reports that the host could not establish what the caller
// is running. NOT a denial: the caller may be perfectly approved.
var ErrExecutionUnbound = errors.New("cannot establish the caller's execution, so no capability may be minted")

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
}

// ExecutionReviewer is the independent source: a TokenReview, and a pod read.
//
// Review and read are separate because the second needs the first's answer — the
// pod is read by the name and namespace the TOKEN named, so a caller cannot
// point the host at a different pod.
type ExecutionReviewer interface {
	// NativeDevelopment is true only for a non-pod local stand-in constructed
	// by the infrastructure adapter. Production always requires a mesh presenter.
	NativeDevelopment() bool
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

// ErrUnknownExecutionPrincipal reports a principal the registry holds no
// workload for: nothing is known about it, so nothing can be bound to it.
var ErrUnknownExecutionPrincipal = errors.New("principal is not known to the execution authority")

// executionTokenAudience is the audience a module's projected token is minted
// for: this host.
const executionTokenAudience = "accounts"

// BindExecution establishes which workload a caller is and holds it against the
// workloads the principal's applied manifest lists. The caller passes its mint
// snapshot; the workload-owner lookup and the declared container both read
// that same registry.
//
// THE ORDER IS LOAD-BEARING, and it is: authenticate, then resolve the
// reviewed workload to its principal, then read the pod the token named.
// Reading the pod first would mean reading a pod the caller named, which is a
// caller-supplied input; the token is what makes the pod reference
// trustworthy.
func (s *Service) BindExecution(
	ctx context.Context, snapshot *installedModuleRegistry, principalID, token string,
) (*ExecutionIdentity, error) {
	if s.executionReviewer == nil {
		return nil, fmt.Errorf("%w: this host has no execution reviewer wired", ErrExecutionUnbound)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: no token presented", ErrExecutionNotBoundToken)
	}

	// THE DECLARED WORKLOADS, read before the token is reviewed, because they
	// are what the review will be held against. A principal this host has no
	// declaration for is refused as unknown.
	// A workload stated in part would check the parts it has and skip the
	// rest, so one is refused like none.
	grant, declared := snapshot.registry[principalID]
	if !declared || len(grant.Workloads) == 0 || slices.ContainsFunc(grant.Workloads, func(w ModuleWorkload) bool { return !w.declared() }) {
		return nil, fmt.Errorf("%w: %s declares no complete workload identity", ErrUnknownExecutionPrincipal, principalID)
	}
	modules := snapshot.registry

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

	if !s.executionReviewer.NativeDevelopment() {
		presenter := VerifiedWorkloadPresenter(ctx)
		identity, parseErr := ParseKubernetesSPIFFEID(presenter)
		if parseErr != nil || identity.Namespace != reviewed.Namespace || identity.ServiceAccount != reviewed.ServiceAccount {
			return nil, fmt.Errorf("%w: authenticated presenter does not match the applied and reviewed workload", ErrExecutionIdentityMismatch)
		}
	}

	// Mesh identity identifies a ServiceAccount, not an individual pod. Keep
	// the projected-token lifetime and pod UID checks as well.
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
			"%w: %s is declared to run as %s, the presented token authenticates %s/%s",
			ErrExecutionIdentityMismatch, principalID, describeWorkloads(grant.Workloads),
			reviewed.Namespace, reviewed.ServiceAccount)
	}
	// The container comes from the manifest's workload, never from the
	// caller.
	containerName := ""
	for _, workload := range grant.Workloads {
		if workload.Namespace == reviewed.Namespace && workload.ServiceAccount == reviewed.ServiceAccount {
			containerName = workload.Container
		}
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
	if _, err := authenticatingContainer(pod, containerName, reviewed); err != nil {
		return nil, err
	}
	return &ExecutionIdentity{
		PrincipalID:    principalID,
		Namespace:      reviewed.Namespace,
		ServiceAccount: reviewed.ServiceAccount,
		PodName:        reviewed.PodName,
		PodUID:         reviewed.PodUID,
		ContainerName:  containerName,
	}, nil
}

// describeWorkloads spells a principal's workloads for a refusal.
func describeWorkloads(workloads []ModuleWorkload) string {
	described := make([]string, 0, len(workloads))
	for _, workload := range workloads {
		described = append(described, workload.Namespace+"/"+workload.ServiceAccount)
	}
	return strings.Join(described, ", ")
}

// principalsDeclaringWorkload returns, sorted, every principal whose declared
// workload runs as serviceAccount in namespace. The container is deliberately
// not part of the key: a token authenticates the ServiceAccount, and every
// container in a pod can read it.
func principalsDeclaringWorkload(modules ModulePrincipalRegistry, namespace, serviceAccount string) []string {
	var owners []string
	for id, grant := range modules {
		for _, workload := range grant.Workloads {
			if workload.Namespace == namespace && workload.ServiceAccount == serviceAccount {
				owners = append(owners, id)
				break
			}
		}
	}
	slices.Sort(owners)
	return owners
}

// authenticatingContainer holds a pod read to what an execution-bound mint
// requires, and returns the authenticating container.
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
// WHY NOT ADMIT A SIDECAR. The manifest names one authenticating container per
// workload and nothing about the pod's others; every container can read the
// projected token, so a container the manifest does not name is refused rather
// than admitted by name.
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
	return app, nil
}

// SetExecutionBinding wires the workload reviewer every module mint binds
// through. Boot wiring.
func (s *Service) SetExecutionBinding(reviewer ExecutionReviewer) {
	s.executionReviewer = reviewer
}
