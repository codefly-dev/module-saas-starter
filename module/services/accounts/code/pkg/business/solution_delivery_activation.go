package business

import (
	"context"
	"errors"
	"fmt"

	"github.com/codefly-dev/core/solutionhost"
)

// Activation: the one shape in which delivered authority is active (issue #952).
//
// WHAT THIS CLOSES. The delivery endpoint accepted authority documents and
// persisted them, and nothing ever read one. There was no call site for core's
// Activate anywhere in this host, so the authority half of the lifecycle was a
// write-only inbox: an operator could deliver a reviewed, signed, envelope-bound
// grant and the host would store it and behave exactly as if it had not.
//
// NEITHER HALF ACTIVATES ALONE, which is why this takes a binding and a build
// rather than offering a way to ask the question of a document. An authority
// document on its own grants nothing, because nothing says the build it approves
// is the build that is present; a presence document on its own grants nothing,
// because nothing says anyone was granted authority over it. The build is a
// parameter rather than something read out of either document, because it is the
// thing being asked about — reading it out of the document that approves it would
// make the question answer itself.
//
// THE CEILING IS NOT HERE. core's Envelope is supplied by the caller and never
// read out of a document, because "an envelope a document carried would be a
// document declaring its own ceiling" — and ValidateAgainst tests a document's
// grants and approved build against the envelope's, so an envelope assembled
// from the delivery tree answers itself. The host reads it from the trust anchor,
// a platform-owned path neither delivery writer can write; see
// infra.ReadSolutionAuthorityEnvelope.
//
// THIS HOST APPLIES AUTHORITY TO NOTHING, and that is stated rather than
// defaulted. core's ActivationRequest requires either the applied authority
// record or an explicit marker that none exists, because "nothing applied" is
// the most permissive input the call takes. This host holds no applied authority
// record — there is no durable AppliedAuthority state and nothing to reconcile
// it into — so the marker is the truth, and the replay protection core's fold
// would otherwise give comes from the inbox instead: the desired set is the
// NEWEST generation per document, so a genuinely signed older generation
// re-delivered never becomes the answer.
//
// One thing the fold does that the inbox does not do by itself is refuse an
// authority WITHDRAWN under one id and re-signed under another — core keys that
// on the binding precisely so a rename cannot reinstate it. So this does it
// explicitly: every delivered authority document over the binding is read, and a
// tombstone among them refuses activation for that binding permanently. Nothing
// deletes from the inbox, so a withdrawal stays visible forever, which is what
// makes that check equivalent rather than merely similar.

var (
	// ErrSolutionAuthorityCeilingUnavailable reports that no authority envelope
	// is delivered to this host, so it can answer no authority question.
	//
	// Distinct from "not activated" on purpose: nothing was judged. A host with
	// no ceiling has not decided that a grant is absent, it has decided
	// nothing, and an operator fixes those two in different places.
	ErrSolutionAuthorityCeilingUnavailable = errors.New("this host has no authority envelope, so it activates nothing")

	// ErrSolutionAuthorityNotDelivered reports that one half is missing from the
	// inbox: no authority document over this binding, or no presence document
	// for it.
	ErrSolutionAuthorityNotDelivered = errors.New("solution authority or presence has not been delivered for this binding")

	// ErrSolutionAuthorityWithdrawn reports that authority over this binding was
	// withdrawn. TERMINAL for the binding, including for an authority re-signed
	// under a new id: a withdrawal is a statement about the binding, and
	// renaming the grant does not reinstate it.
	ErrSolutionAuthorityWithdrawn = errors.New("solution authority over this binding was withdrawn")

	// ErrSolutionAuthorityAmbiguous reports more than one live authority
	// document over one binding. Refused rather than resolved: whichever one a
	// host picked would be a choice nobody reviewed.
	ErrSolutionAuthorityAmbiguous = errors.New("more than one live solution authority document is delivered over this binding")
)

// SolutionAuthorityActivation answers whether delivered authority is active for
// a binding at a build.
//
// It is built on the reconciler rather than beside it, and that is not
// convenience: the reconciler already holds the four policy inputs activation
// needs — this host's coordinate, the ownership domains it accepts, which signer
// may speak for which domain, and the bundle verifier — and a second copy of any
// of them is a second answer to "what does this host accept".
type SolutionAuthorityActivation struct {
	reconciler *SolutionHostBindingReconciler
	envelope   solutionhost.Envelope
}

// newSolutionAuthorityActivation builds the activation reader, which the
// reconciler's constructor owns: a caller that could build one with a different
// coordinate, a different signer policy or a different verifier than the
// reconciler's would be a second answer to what this host accepts. Reach it
// through SolutionHostBindingReconciler.AuthorityActivation.
//
// A zero-revision envelope is accepted and makes every activation refuse with
// ErrSolutionAuthorityCeilingUnavailable, rather than refusing at construction.
// The distinction matters: a host that reconciles presence and has no ceiling
// delivered is a correct, complete deployment — authority is simply not a
// question it can answer — while a host that refused to start would make the
// presence half depend on the authority half being configured.
func newSolutionAuthorityActivation(
	reconciler *SolutionHostBindingReconciler, envelope solutionhost.Envelope,
) (*SolutionAuthorityActivation, error) {
	if reconciler == nil {
		return nil, errors.New("solution authority activation requires the declared-presence reconciler, which holds this host's coordinate, domains, signer policy and verifier")
	}
	return &SolutionAuthorityActivation{reconciler: reconciler, envelope: envelope}, nil
}

// Activate reports whether the delivered authority over binding and the
// delivered presence for it form a matched tuple for build.
//
// Both halves are RE-VERIFIED out of the inbox on every call rather than trusted
// as stored, which is the same rule the reconcile pass follows and for the same
// reason: a signer removed from the allowlist must stop being able to keep its
// documents effective, instead of its last delivery remaining authoritative
// forever.
func (a *SolutionAuthorityActivation) Activate(
	ctx context.Context, binding string, build solutionhost.ImageDigest,
) (solutionhost.Activation, error) {
	if a == nil || a.reconciler == nil {
		return solutionhost.Activation{}, ErrSolutionAuthorityCeilingUnavailable
	}
	if a.envelope.Revision == 0 {
		return solutionhost.Activation{}, fmt.Errorf("%w: binding %q", ErrSolutionAuthorityCeilingUnavailable, binding)
	}

	authority, err := a.deliveredAuthority(ctx, binding)
	if err != nil {
		return solutionhost.Activation{}, err
	}
	presence, err := a.deliveredPresence(ctx, binding)
	if err != nil {
		return solutionhost.Activation{}, err
	}
	// The applied-presence read is gone with the fields it fed. It is NOT
	// replaced by something weaker: core v0.9.0 judges the delivered tuple, and
	// the one thing this host still owes — finding a tombstone delivered under a
	// different authority id — is done by deliveredAuthority reading every
	// authority document over the binding, not by reporting applied state.
	host := a.reconciler.host
	return solutionhost.Activate(solutionhost.ActivationRequest{
		Authority:       authority,
		Presence:        presence,
		Coordinate:      host.Coordinate,
		Build:           build,
		Envelope:        a.envelope,
		DomainsBySigner: host.DomainsBySigner,
	})
}

// Core v0.9.0 removed `Applied`, `FirstAuthorityRecord`, `AppliedPresence` and
// `FirstPresenceRecord` from ActivationRequest: activation no longer takes the
// host's applied state at all. The request is now the tuple plus the questions
// only the caller can answer — the coordinate and the build — which core
// documents as deliberate, because "reading the host out of the halves that
// claim it would make the question answer itself".
//
// That does NOT retire the withdrawal check below. Core's activation judges the
// documents it is handed; it does not know that a tombstone for this binding was
// delivered under a different authority id, because nothing in the request
// carries the inbox. Reading every authority document over the binding is still
// this host's job, and it is the one part of the old applied-record argument that
// survives the field removal.
//
// deliveredAuthority reads the live authority document over one binding.
//
// EVERY delivered authority document is read, not just the one that matches.
// Two reasons, and the first is the hole: a withdrawal is recorded under the
// authority id it was delivered as, so finding "the authority over this binding"
// by looking only at a candidate would miss a tombstone under a different id —
// which is exactly the rename that core's binding-keyed fold exists to refuse.
// The second is that two live documents over one binding have no right answer,
// and a host that read the first match would pick one silently.
func (a *SolutionAuthorityActivation) deliveredAuthority(
	ctx context.Context, binding string,
) (*solutionhost.DeliveredAuthority, error) {
	records, err := a.newestDelivered(ctx, SolutionDeliveryAuthority)
	if err != nil {
		return nil, err
	}
	var live []*solutionhost.DeliveredAuthority
	for _, record := range records {
		delivered, err := solutionhost.VerifyDeliveredAuthority(ctx, carrierOf(record), a.reconciler.verifier)
		if err != nil {
			// FAIL CLOSED OVER THE WHOLE READ rather than skipping the row.
			// The binding a row is over is only knowable by reading it, so a
			// row this host can no longer verify leaves the authority state for
			// EVERY binding unestablished — including whether a withdrawal is
			// among them. Skipping it would make an unverifiable tombstone
			// disappear, which is the one row whose disappearance grants
			// something.
			return nil, fmt.Errorf("re-verify delivered authority %s generation %d: %w",
				record.DocumentID, record.Generation, err)
		}
		document, err := delivered.Document()
		if err != nil {
			return nil, fmt.Errorf("read delivered authority %s generation %d: %w",
				record.DocumentID, record.Generation, err)
		}
		if document.PresenceBinding != binding {
			continue
		}
		if document.Removed {
			return nil, fmt.Errorf("%w: authority %q was withdrawn at generation %d, and a withdrawal is terminal for binding %q — re-signing under a new authority id does not reinstate it",
				ErrSolutionAuthorityWithdrawn, document.Authority, document.Generation, binding)
		}
		live = append(live, delivered)
	}
	switch len(live) {
	case 0:
		return nil, fmt.Errorf("%w: no authority document is delivered over binding %q",
			ErrSolutionAuthorityNotDelivered, binding)
	case 1:
		return live[0], nil
	}
	return nil, fmt.Errorf("%w: %d over binding %q", ErrSolutionAuthorityAmbiguous, len(live), binding)
}

// deliveredPresence reads the newest delivered presence document for one
// binding. The inbox is keyed by the binding for this kind, so this is a lookup
// rather than a scan.
func (a *SolutionAuthorityActivation) deliveredPresence(
	ctx context.Context, binding string,
) (*solutionhost.Delivered, error) {
	records, err := a.newestDelivered(ctx, SolutionDeliveryPresence)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.DocumentID != binding {
			continue
		}
		delivered, err := solutionhost.VerifyDelivered(ctx, carrierOf(record), a.reconciler.verifier)
		if err != nil {
			return nil, fmt.Errorf("re-verify delivered presence %s generation %d: %w",
				record.DocumentID, record.Generation, err)
		}
		return delivered, nil
	}
	return nil, fmt.Errorf("%w: no presence document is delivered for binding %q",
		ErrSolutionAuthorityNotDelivered, binding)
}

func (a *SolutionAuthorityActivation) newestDelivered(
	ctx context.Context, kind SolutionDeliveryKind,
) ([]*SolutionDeliveryRecord, error) {
	service := a.reconciler.service
	if service.deliveryStore == nil {
		return nil, fmt.Errorf("%w: this host has no delivery inbox wired", ErrSolutionAuthorityCeilingUnavailable)
	}
	var records []*SolutionDeliveryRecord
	if err := service.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		records, err = service.deliveryStore.ListNewestDeliveredDocuments(ctx, kind)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the %s delivery inbox: %w", kind, err)
	}
	return records, nil
}

// carrierOf rebuilds the carrier a row arrived as, so the stored bytes go back
// through core's verification rather than being trusted because they are stored.
// core validates the carrier's own invariants, so a row that cannot be a carrier
// is refused by the same check that refuses one off the wire.
func carrierOf(record *SolutionDeliveryRecord) *solutionhost.Signed {
	return &solutionhost.Signed{
		Schema:   solutionhost.SchemaSignedV1,
		Document: record.Payload,
		Bundle:   record.Bundle,
	}
}
