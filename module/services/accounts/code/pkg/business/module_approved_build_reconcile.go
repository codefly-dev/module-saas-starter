package business

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/codefly-dev/core/solutionhost"
)

// Reconstructing the approved-build view from the delivered authority inbox,
// which is what makes execution-bound minting answerable in production.
//
// WHAT THIS CLOSES. `MonotonicApprovedBuilds` holds the authoritative view and
// enforces the ordering rule over it, and nothing wrote to it. Every principal
// was therefore UNKNOWN, which `ApprovedBuild` answers with
// ErrUnknownExecutionPrincipal — so `BindExecution` refused every caller and the
// whole execution-binding mechanism was unreachable from a running host. It was
// built, unit-tested and dead, which is the worst of the three states: a
// reviewer reads the tests and the guard and concludes the check is enforced.
//
// WHY RECONSTRUCTION RATHER THAN A WRITE PER DELIVERY. A withdrawal has to
// REMOVE an approved build, and `MonotonicApprovedBuilds` cannot: it has Approve
// and Declare and no third verb, by design — core's own guidance is that "a
// source that genuinely must rewind is reconstructing state rather than
// recording it, and belongs behind a fresh instance". So each pass builds a
// fresh view from the whole inbox and swaps it in. A tombstoned authority simply
// has no entry in the new view, and the principal goes back to UNKNOWN.
//
// AND WHY THE HIGH-WATER MARK SURVIVES THE SWAP. Applying that guidance naively
// loses the thing the guard exists for. A fresh instance holds no previous
// record, so between passes there is nothing for a rewind to be compared
// against: delivery could ship generation 4, then generation 3, and each pass
// would accept its own input as the first thing it had ever seen. The ordering
// rule is a property of the SEQUENCE OF PASSES, not of one pass, so the marks
// outlive the view they are used to build. Reconstruct the SET; keep the
// ORDERING.
//
// A TOMBSTONE MAKES A PRINCIPAL UNKNOWN, NOT UNBOUND. These are the two
// answers it would be easy to conflate, and only one of them is safe.
// `Declare` records "known, bears no execution", which mints a capability
// carrying no execution that every verifier accepts — the correct answer for a
// HUMAN session, and the wrong one for a module whose authority was withdrawn.
// So this reconciler never calls Declare: a principal is either approved for a
// build or absent, and absent is a refusal. The human path is not fed from this
// inbox at all.
//
// THE INCARNATION IS THE AUTHORITY DOCUMENT'S GENERATION, which core documents
// as "strictly monotonic per authority ID and starts at 1". That satisfies both
// clauses of the contract for free — a new approved build arrives as a new
// generation, so a change of digest necessarily advances the counter — and it
// avoids inventing a counter this host would then have to keep.
//
// PER AUTHORITY ID is the catch, and it is why the ambiguity check below exists.
// Two authority documents with different authority IDs have INDEPENDENT
// generation sequences, so if both name one principal their numbers are not
// comparable: the view would flap between them, and each flap either trips the
// rewind guard or — when the numbers happen to ascend — silently reseals the
// principal to the other document's build. A principal named by more than one
// live authority document is refused by name rather than resolved, for the same
// reason two live authority documents over one binding are.

// ErrApprovedBuildAmbiguous reports a principal that more than one live
// authority document grants to, whose generations are therefore incomparable.
var ErrApprovedBuildAmbiguous = errors.New("more than one live authority document grants to this principal, so its approved build has no single generation")

// ErrApprovedBuildInboxUnreadable reports that the pass could not establish the
// inbox, so no view was swapped in.
//
// Distinct from an empty view: nothing was judged. The previous view keeps
// serving, which is the same posture the presence reconciler takes for an
// unreadable mount — an unreadable inbox is not an empty desired set.
var ErrApprovedBuildInboxUnreadable = errors.New("the delivered authority inbox could not be established, so the approved-build view is unchanged")

// ApprovedBuildReconciler rebuilds the approved-build view from the delivered
// authority inbox, keeping the ordering guarantee across passes.
//
// It is the ExecutionAuthority a host wires: reads go to whichever view the last
// successful pass installed, so a caller never observes a half-built one.
type ApprovedBuildReconciler struct {
	activation *SolutionAuthorityActivation

	mutex sync.RWMutex
	// view is what reads are served from. Never mutated in place: a pass
	// builds a replacement and swaps the pointer, so a read either sees the
	// whole previous pass or the whole new one.
	view *MonotonicApprovedBuilds
	// marks is the high-water mark per principal, and it is deliberately NOT
	// part of the view. It records the highest incarnation this host has ever
	// SERVED for a principal, so a later pass carrying a lower one is a
	// refusal rather than a fresh start. It only grows; a principal whose
	// authority is withdrawn keeps its mark, because re-delivering the
	// withdrawn generation must not become acceptable again.
	marks map[string]uint64
}

// NewApprovedBuildReconciler builds the reconciler over the activation reader.
//
// It takes the ACTIVATION reader rather than the delivery store directly,
// because that is the one place that re-verifies a delivered authority document
// against the host's current signer policy. Reading the store itself would make
// this a second answer to "which delivered documents does this host accept", and
// a signer removed from the allowlist would go on approving builds here after it
// had stopped being able to activate anything.
func NewApprovedBuildReconciler(activation *SolutionAuthorityActivation) (*ApprovedBuildReconciler, error) {
	if activation == nil {
		return nil, errors.New("the approved-build reconciler requires the authority activation reader, which owns document re-verification")
	}
	return &ApprovedBuildReconciler{
		activation: activation,
		// An empty view, which answers ErrUnknownExecutionPrincipal for every
		// principal. A host that has reconciled nothing refuses rather than
		// minting unbound capabilities for everyone, and that is the state
		// between process start and the first successful pass.
		view:  NewMonotonicApprovedBuilds(),
		marks: map[string]uint64{},
	}, nil
}

// ApprovedBuild serves from the installed view.
//
// The three answers are the view's: a record, ErrNoApprovedBuild for a known
// principal bearing none, ErrUnknownExecutionPrincipal otherwise.
func (r *ApprovedBuildReconciler) ApprovedBuild(
	ctx context.Context, principalID string,
) (ApprovedDigest, uint64, error) {
	r.mutex.RLock()
	view := r.view
	r.mutex.RUnlock()
	return view.ApprovedBuild(ctx, principalID)
}

// RunOnce rebuilds the view from the inbox and installs it.
//
// AN UNREADABLE INBOX INSTALLS NOTHING. Which principals a row grants to is
// only knowable by reading it, so a row this host can no longer verify leaves
// the approved state for EVERY principal unestablished — and the one row whose
// disappearance grants something is a tombstone. Skipping it would resurrect
// the build it withdrew. So verification failure returns before anything is
// built and the previous view keeps serving.
//
// A principal the inbox describes INCOHERENTLY is a narrower failure, and it is
// handled per principal rather than per pass: see the comment at the install.
func (r *ApprovedBuildReconciler) RunOnce(ctx context.Context) error {
	// A HOST THAT ANSWERS NO AUTHORITY QUESTION HAS NOTHING TO REFRESH, and
	// that is a no-op rather than a failure.
	//
	// This returned the ceiling-unavailable error as a pass failure, and a test
	// found it: every presence reconcile on a host with no envelope — or with no
	// delivery inbox wired — started failing. That is precisely the coupling
	// this design forbids. `newSolutionAuthorityActivation` says it outright: a
	// host that reconciles presence with no ceiling delivered is a CORRECT,
	// COMPLETE deployment, and making the presence half depend on the authority
	// half being configured is the wrong direction.
	//
	// Nothing is hidden by returning early. The view stays empty, so every
	// principal is UNKNOWN and every module mint is refused; and the operator
	// learns by name at the point of use, where activation already refuses with
	// ErrSolutionAuthorityCeilingUnavailable. The envelope is read once at boot
	// and cannot change under a running process, so there is no case where a
	// host had a ceiling and this skipped a refresh it owed.
	if r.activation.envelope.Revision == 0 {
		return nil
	}

	documents, err := r.activation.liveAuthorityDocuments(ctx)
	switch {
	case errors.Is(err, ErrSolutionAuthorityCeilingUnavailable):
		// No delivery inbox wired: the same "answers nothing" posture as no
		// envelope, reached through the other half of the configuration.
		return nil
	case err != nil:
		// An inbox that EXISTS and could not be read is a real failure, and the
		// distinction is the whole point: a row this host can no longer verify
		// leaves the approved state for every principal unestablished, and the
		// one row whose disappearance grants something is a tombstone.
		return fmt.Errorf("%w: %w", ErrApprovedBuildInboxUnreadable, err)
	}

	// THE CEILING, before anything is approved. An authority document is
	// signed, which makes it authentic and says nothing about whether what it
	// claims is inside the ceiling a platform administrator wrote. Core keeps
	// those as separate steps and says so — "whether it is inside a ceiling is
	// ValidateAgainst" — and the clause that matters here is the last one: the
	// build it is approved for must be one THE ENVELOPE approved. Without this
	// call a delivery writer could approve any image at all by signing a
	// document that names it, which is the entire check this view feeds.
	//
	inside := make([]*solutionhost.AuthorityDocument, 0, len(documents))
	var outside []error
	for _, document := range documents {
		if err := document.ValidateAgainst(r.activation.envelope); err != nil {
			// Per document, not per pass: a document outside the ceiling is one
			// delivery writer's problem, and the principals it names simply get
			// no entry. Refusing the whole inbox over it would let any
			// out-of-ceiling delivery deny every module on the host.
			outside = append(outside, fmt.Errorf("authority %q generation %d is outside this host's ceiling: %w",
				document.Authority, document.Generation, err))
			continue
		}
		inside = append(inside, document)
	}
	documents = inside

	// Which principals more than one live document grants to. Collected over
	// the whole inbox before anything is approved, because the second document
	// naming a principal is what makes the FIRST one ambiguous too — resolving
	// as the rows arrive would approve from whichever happened to be read
	// first.
	grantedBy := map[string][]string{}
	for _, document := range documents {
		for _, principal := range document.Principals {
			grantedBy[principal.Principal] = append(grantedBy[principal.Principal], document.Authority)
		}
	}

	next := NewMonotonicApprovedBuilds()
	marks := make(map[string]uint64, len(r.marks))
	r.mutex.RLock()
	for principal, mark := range r.marks {
		marks[principal] = mark
	}
	r.mutex.RUnlock()

	failures := outside
	for _, document := range documents {
		for _, principal := range document.Principals {
			id := principal.Principal
			if authorities := grantedBy[id]; len(authorities) > 1 {
				sort.Strings(authorities)
				failures = append(failures, fmt.Errorf("%w: principal %q is granted by authorities %v",
					ErrApprovedBuildAmbiguous, id, authorities))
				continue
			}
			if mark, had := marks[id]; had && document.Generation < mark {
				// The rewind a fresh instance cannot see. Reported rather
				// than clamped: a clamp would serve a value delivery did not
				// intend and delivery would never learn.
				failures = append(failures, fmt.Errorf(
					"%w: authority %q delivers generation %d for principal %q, below the %d already served",
					ErrApprovedBuildRewind, document.Authority, document.Generation, id, mark))
				continue
			}
			if err := next.Approve(id, ApprovedBuildRecord{
				Digest:      ApprovedDigest(document.ApprovedBuild),
				Incarnation: document.Generation,
			}); err != nil {
				failures = append(failures, fmt.Errorf("approve %q from authority %q generation %d: %w",
					id, document.Authority, document.Generation, err))
				continue
			}
			marks[id] = document.Generation
		}
	}

	// A REFUSED PRINCIPAL IS OMITTED; THE REST INSTALL. The first version of
	// this installed nothing when any principal was refused, on the argument
	// that installing the coherent part of an incoherent inbox is how a
	// withheld document becomes a quietly narrower system. A test found that
	// wrong, and the reason is the inbox's shape: it is HOST-WIDE, so an
	// authority document delivered for one solution sits in the same inbox as
	// every other's. Withholding the whole pass therefore makes one solution's
	// bad delivery deny service to every module on the host — a host-wide
	// outage manufactured out of one unrelated document.
	//
	// Omitting the principal is no less fail-closed for the principal that is
	// actually in doubt: it has no entry in the new view, so it reads back as
	// UNKNOWN and every mint for it is refused. What it does not do is couple
	// that refusal to principals whose documents say exactly one thing. The
	// reasons are still returned, so an operator sees each refusal rather than
	// inferring it from a principal that stopped working.
	//
	// The one failure that IS whole-pass is an unreadable inbox, and it is
	// handled above by returning before any of this: there, nothing was
	// established about any principal, so the previous view keeps serving.
	r.mutex.Lock()
	r.view = next
	r.marks = marks
	r.mutex.Unlock()
	return errors.Join(failures...)
}

var _ ExecutionAuthority = (*ApprovedBuildReconciler)(nil)
