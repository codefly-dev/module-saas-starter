package business

import (
	"context"
	"errors"
	"fmt"
	"slices"

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
// from the delivery tree answers itself. The host reads the CURRENT module
// envelope — the record a platform super_admin writes (module_envelope.go) —
// on every call, and projects it for the document being judged.
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
	// envelopes is where the CURRENT ceiling is read from, on every call. Nil
	// is a host with no ceiling: it activates nothing.
	envelopes ModuleEnvelopeSource
}

// newSolutionAuthorityActivation builds the activation reader, which the
// reconciler's constructor owns: a caller that could build one with a different
// coordinate, a different signer policy or a different verifier than the
// reconciler's would be a second answer to what this host accepts. Reach it
// through SolutionHostBindingReconciler.AuthorityActivation.
//
// No envelope — no source, or none written yet — is accepted and makes every
// activation refuse with ErrSolutionAuthorityCeilingUnavailable, rather than
// refusing at construction.
// The distinction matters: a host that reconciles presence and has no ceiling
// delivered is a correct, complete deployment — authority is simply not a
// question it can answer — while a host that refused to start would make the
// presence half depend on the authority half being configured.
func newSolutionAuthorityActivation(
	reconciler *SolutionHostBindingReconciler, envelopes ModuleEnvelopeSource,
) (*SolutionAuthorityActivation, error) {
	if reconciler == nil {
		return nil, errors.New("solution authority activation requires the declared-presence reconciler, which holds this host's coordinate, domains, signer policy and verifier")
	}
	return &SolutionAuthorityActivation{reconciler: reconciler, envelopes: envelopes}, nil
}

// currentEnvelope reads the ceiling as it stands now. Nil with no error is a
// host with no ceiling — no source wired, or no revision written — which
// activates and grants nothing; an error is a ceiling that could not be
// established, which a caller must not read as "none".
func (a *SolutionAuthorityActivation) currentEnvelope(ctx context.Context) (*ModuleEnvelope, error) {
	if a == nil || a.envelopes == nil {
		return nil, nil
	}
	envelope, err := a.envelopes.CurrentModuleEnvelope(ctx)
	if errors.Is(err, ErrModuleEnvelopeUnavailable) {
		return nil, nil
	}
	return envelope, err
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
	envelope, err := a.currentEnvelope(ctx)
	if err != nil {
		return solutionhost.Activation{}, fmt.Errorf("read the authority ceiling for binding %q: %w", binding, err)
	}
	if envelope == nil {
		return solutionhost.Activation{}, fmt.Errorf("%w: binding %q", ErrSolutionAuthorityCeilingUnavailable, binding)
	}

	authority, err := a.deliveredAuthority(ctx, binding)
	if err != nil {
		return solutionhost.Activation{}, err
	}
	authorityDocument, err := authority.Document()
	if err != nil {
		return solutionhost.Activation{}, fmt.Errorf("read delivered authority over binding %q: %w", binding, err)
	}
	presence, err := a.deliveredPresence(ctx, binding)
	if err != nil {
		return solutionhost.Activation{}, err
	}
	host := a.reconciler.host
	records, err := a.appliedState(ctx)
	if err != nil {
		return solutionhost.Activation{}, err
	}
	return solutionhost.Activate(solutionhost.ActivationRequest{
		Authority:       authority,
		Presence:        presence,
		Coordinate:      host.Coordinate,
		Build:           build,
		Envelope:        envelope.authorityEnvelopeFor(authorityDocument),
		DomainsBySigner: host.DomainsBySigner,
		// The ownership domains this host ACCEPTS, which is a different
		// question from which signer may speak for one. Core requires it for
		// the reason it requires it of Admit: activation checked who may speak
		// for a domain and never whether the host accepts the domain at all,
		// so an activation succeeded for a domain the host does not list while
		// Admit refused the very same documents.
		Domains: host.Domains,
		// The applied state, which CORE READS ITSELF through this reader.
		Records: records,
	})
}

// appliedState builds the reader core reads the host's applied state through.
//
// It pre-loads the records because the interface takes no context: a reader that
// closed over one and queried per call would run a database read inside core's
// judgement, where a failure has nowhere to go but a bool.
func (a *SolutionAuthorityActivation) appliedState(
	ctx context.Context,
) (*appliedSolutionState, error) {
	records, err := a.reconciler.service.ListSolutionHostBindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read applied solution host bindings: %w", err)
	}
	byBinding := make(map[string]*SolutionHostBindingRecord, len(records))
	for _, record := range records {
		byBinding[record.BindingID] = record
	}
	return &appliedSolutionState{byBinding: byBinding}, nil
}

// appliedSolutionState answers core's two applied-state questions.
//
// WHY THIS EXISTS AT ALL, since an earlier version of this file said it did not.
// That comment read core v0.9.0's removal of `Applied`, `FirstAuthorityRecord`,
// `AppliedPresence` and `FirstPresenceRecord` and concluded activation no longer
// takes the host's applied state. It does: core replaced four caller-SUPPLIED
// fields with one reader it calls ITSELF, having derived the binding from the
// attested presence bytes — so the key cannot be the caller's choice and "no
// record" cannot be the caller's assertion. Passing neither it nor Domains made
// core refuse every tuple by name, which is how this was found.
type appliedSolutionState struct {
	byBinding map[string]*SolutionHostBindingRecord
}

// PresenceRecord answers from the durable applied generation.
func (s *appliedSolutionState) PresenceRecord(binding string) (solutionhost.Applied, bool, error) {
	record, ok := s.byBinding[binding]
	if !ok {
		return solutionhost.Applied{}, false, nil
	}
	applied, ok := record.AppliedState()
	return applied, ok, nil
}

// AuthorityRecord reports that there is none, for every binding.
//
// THIS HOST HOLDS NO APPLIED AUTHORITY STATE. There is no durable
// AppliedAuthority relation and nothing to reconcile one into, so "no record" is
// the truth here rather than a marker — and it is the truth UNIFORMLY, which is
// what makes it safe. Core's reason for taking the reader was that a host keyed
// by AUTHORITY ID truthfully found no record for a RENAMED authority and
// activated it over a withdrawn binding: the bypass needed a lookup that could
// miss. A reader that answers "none" for every binding cannot miss selectively.
//
// The replay protection core's fold would otherwise give comes from the inbox
// instead, and that is not a weaker substitute for the renaming case
// specifically: deliveredAuthority reads EVERY delivered authority document over
// the binding and a tombstone among them refuses activation permanently,
// whatever id it was signed under. Nothing deletes from the inbox, so a
// withdrawal stays visible forever.
//
// If this host ever grows durable applied authority state, this must answer from
// it, keyed on the BINDING and never on the authority id.
func (s *appliedSolutionState) AuthorityRecord(string) (solutionhost.AppliedAuthority, bool, error) {
	return solutionhost.AppliedAuthority{}, false, nil
}

var _ solutionhost.AppliedStateReader = (*appliedSolutionState)(nil)

// Core v0.9.0 replaced `Applied`, `FirstAuthorityRecord`, `AppliedPresence` and
// `FirstPresenceRecord` with ONE `Records AppliedStateReader` that core calls
// itself, keyed on a binding it derives from the attested presence bytes. An
// earlier version of this comment read the removal and missed the replacement,
// concluding activation no longer takes applied state; it does, and passing
// neither it nor `Domains` made core refuse every tuple.
//
// None of that retires the withdrawal check below. Core's activation judges the
// documents it is handed and the records it reads; it does not know that a
// tombstone for this binding was delivered under a different authority id,
// because nothing in the request carries the inbox — and this host's
// AuthorityRecord has none to give. Reading every authority document over the
// binding is still this host's job.
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
	delivered, err := a.verifiedAuthority(ctx)
	if err != nil {
		return nil, err
	}
	withdrawn, err := a.withdrawnBindings(ctx)
	if err != nil {
		return nil, err
	}
	if authority, gone := withdrawn[binding]; gone {
		return nil, fmt.Errorf("%w: authority %q withdrew binding %q, and a withdrawal is terminal for it — a later generation or a new authority id does not reinstate it",
			ErrSolutionAuthorityWithdrawn, authority, binding)
	}
	var live []*solutionhost.DeliveredAuthority
	for _, one := range delivered {
		if one.document.PresenceBinding != binding {
			continue
		}
		if one.document.Removed {
			return nil, fmt.Errorf("%w: authority %q was withdrawn at generation %d, and a withdrawal is terminal for binding %q — re-signing under a new authority id does not reinstate it",
				ErrSolutionAuthorityWithdrawn, one.document.Authority, one.document.Generation, binding)
		}
		live = append(live, one.delivered)
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

// verifiedAuthorityDocument pairs a re-verified carrier with the document it
// carries, so a caller that needs both does not verify twice — and, more to the
// point, so there is exactly one place where a stored row is turned back into a
// document this host accepts.
type verifiedAuthorityDocument struct {
	delivered *solutionhost.DeliveredAuthority
	document  *solutionhost.AuthorityDocument
}

// verifiedAuthority re-verifies EVERY newest-generation authority row and
// returns what it read, tombstones included.
//
// FAIL CLOSED OVER THE WHOLE READ rather than skipping the row. The binding a
// row is over is only knowable by reading it, so a row this host can no longer
// verify leaves the authority state for EVERY binding unestablished — including
// whether a withdrawal is among them. Skipping it would make an unverifiable
// tombstone disappear, which is the one row whose disappearance grants
// something.
//
// Tombstones are RETURNED rather than filtered here, because the two callers
// need opposite things from them and neither reading is the general one:
// activation treats a tombstone over its binding as terminal, while the
// approved-build view treats it as the removal of a principal's entry. A helper
// that dropped them would silently give the second caller the first caller's
// answer.
func (a *SolutionAuthorityActivation) verifiedAuthority(
	ctx context.Context,
) ([]verifiedAuthorityDocument, error) {
	records, err := a.newestDelivered(ctx, SolutionDeliveryAuthority)
	if err != nil {
		return nil, err
	}
	read := make([]verifiedAuthorityDocument, 0, len(records))
	for _, record := range records {
		delivered, err := solutionhost.VerifyDeliveredAuthority(ctx, carrierOf(record), a.reconciler.verifier)
		if err != nil {
			return nil, fmt.Errorf("re-verify delivered authority %s generation %d: %w",
				record.DocumentID, record.Generation, err)
		}
		document, err := delivered.Document()
		if err != nil {
			return nil, fmt.Errorf("read delivered authority %s generation %d: %w",
				record.DocumentID, record.Generation, err)
		}
		read = append(read, verifiedAuthorityDocument{delivered: delivered, document: document})
	}
	return read, nil
}

// withdrawnBindings reads the WHOLE delivery history and returns every presence
// binding an ADMITTED tombstone withdrew, with the authority that withdrew it.
//
// The whole history, not the newest generation per authority id: a withdrawal
// is terminal for the binding, and the newest-per-id read hides it as soon as a
// later generation of the SAME id is delivered. Nothing deletes from the inbox,
// so the tombstone is still there to be read.
//
// ADMITTED means the tombstone could speak for the binding it names: it targets
// THIS host's coordinate, and it speaks for the binding's ESTABLISHED ownership
// domain — the domain the binding's first delivered presence generation claimed,
// which core then holds every later generation to. A tombstone asserting
// another domain, or another host, withdraws nothing here: otherwise any
// accepted signer for any domain could permanently revoke every other domain's
// bindings by naming them. A binding with no presence delivered yet has no
// established domain, and a tombstone over it waits — it is re-judged on every
// read, so it takes effect once presence establishes its own domain.
//
// Rows are decoded rather than re-verified, and that is deliberate: this read is
// independent of whether the newest documents still verify, so a document that
// can no longer be verified can never hide a withdrawal. Only the fact that a row
// WITHDRAWS is taken from it, which can only remove authority; an admitted
// withdrawal survives an authority rename and its signer's later removal.
func (a *SolutionAuthorityActivation) withdrawnBindings(ctx context.Context) (map[string]string, error) {
	service := a.reconciler.service
	if service.deliveryStore == nil {
		return nil, fmt.Errorf("%w: this host has no delivery inbox wired", ErrSolutionAuthorityCeilingUnavailable)
	}
	var authority, presence []*SolutionDeliveryRecord
	if err := service.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		if authority, err = service.deliveryStore.ListDeliveredDocuments(ctx, SolutionDeliveryAuthority); err != nil {
			return err
		}
		presence, err = service.deliveryStore.ListDeliveredDocuments(ctx, SolutionDeliveryPresence)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the delivery history: %w", err)
	}
	established, err := establishedPresenceDomains(presence)
	if err != nil {
		return nil, err
	}
	coordinate := a.reconciler.host.Coordinate
	withdrawn := map[string]string{}
	for _, record := range authority {
		document, err := solutionhost.AuthorityFromVerified(record.Payload)
		if err != nil {
			return nil, fmt.Errorf("read delivered authority %s generation %d: %w", record.DocumentID, record.Generation, err)
		}
		if !document.Removed || document.Host.Coordinate != coordinate {
			continue
		}
		if domain, known := established[document.PresenceBinding]; !known || domain != document.OwnershipDomain {
			continue
		}
		if _, already := withdrawn[document.PresenceBinding]; !already {
			withdrawn[document.PresenceBinding] = document.Authority
		}
	}
	return withdrawn, nil
}

// establishedPresenceDomains is each presence binding's ownership domain: the
// domain its lowest delivered generation claimed. Core refuses a later
// generation under another domain, so the first claim is the owner.
func establishedPresenceDomains(records []*SolutionDeliveryRecord) (map[string]string, error) {
	lowest := map[string]uint64{}
	established := map[string]string{}
	for _, record := range records {
		document, err := solutionhost.PresenceFromVerified(record.Payload)
		if err != nil {
			return nil, fmt.Errorf("read delivered presence %s generation %d: %w", record.DocumentID, record.Generation, err)
		}
		if generation, seen := lowest[document.Binding]; seen && generation <= document.Generation {
			continue
		}
		lowest[document.Binding] = document.Generation
		established[document.Binding] = document.OwnershipDomain
	}
	return established, nil
}

// ErrSolutionAuthoritySignerNotGranted reports an authority document whose
// signer the host's CURRENT policy no longer lets speak for its ownership
// domain, or whose domain the host no longer accepts. The signature still
// verifies; the policy that made it authoritative has moved.
var ErrSolutionAuthoritySignerNotGranted = errors.New("the authority's signer may no longer speak for its ownership domain on this host")

// ErrSolutionAuthorityUnverifiable reports a newest authority document this
// host can no longer verify. It grants nothing; it does not stop the others.
var ErrSolutionAuthorityUnverifiable = errors.New("the delivered authority no longer verifies")

// liveVerifiedAuthority is one authority document in force, with the signer
// that delivered it.
type liveVerifiedAuthority struct {
	document *solutionhost.AuthorityDocument
	signer   string
}

// liveAuthority returns the authority documents in force, and the reasons the
// others are not.
//
// ORDER IS THE POINT. The withdrawal history is read FIRST, independently of
// the newest documents, so nothing about a newest document — not even its
// failure to verify — can stop a withdrawal being honoured. Then each newest
// document is judged on its own: a tombstone is dropped; a document that no
// longer verifies, is over a withdrawn binding, or whose signer the host's
// current policy no longer admits for its domain is REFUSED, by name, and grants
// nothing — while the rest stay in force. The only error is a read the store
// could not answer; it never comes with a partial answer.
func (a *SolutionAuthorityActivation) liveAuthority(ctx context.Context) ([]liveVerifiedAuthority, []error, error) {
	withdrawn, err := a.withdrawnBindings(ctx)
	if err != nil {
		return nil, nil, err
	}
	records, err := a.newestDelivered(ctx, SolutionDeliveryAuthority)
	if err != nil {
		return nil, nil, err
	}
	host := a.reconciler.host
	var (
		live     []liveVerifiedAuthority
		refusals []error
	)
	for _, record := range records {
		delivered, err := solutionhost.VerifyDeliveredAuthority(ctx, carrierOf(record), a.reconciler.verifier)
		if err != nil {
			refusals = append(refusals, fmt.Errorf("%w: %s generation %d: %w", ErrSolutionAuthorityUnverifiable, record.DocumentID, record.Generation, err))
			continue
		}
		document, err := delivered.Document()
		if err != nil {
			refusals = append(refusals, fmt.Errorf("%w: %s generation %d: %w", ErrSolutionAuthorityUnverifiable, record.DocumentID, record.Generation, err))
			continue
		}
		if document.Removed {
			continue
		}
		if authority, gone := withdrawn[document.PresenceBinding]; gone {
			refusals = append(refusals, fmt.Errorf("%w: authority %q generation %d is over binding %q, which authority %q withdrew",
				ErrSolutionAuthorityWithdrawn, document.Authority, document.Generation, document.PresenceBinding, authority))
			continue
		}
		signer := delivered.DeliveredBy()
		if !slices.Contains(host.Domains, document.OwnershipDomain) || !slices.Contains(host.DomainsBySigner[signer], document.OwnershipDomain) {
			refusals = append(refusals, fmt.Errorf("%w: authority %q generation %d, signer %q, domain %q",
				ErrSolutionAuthoritySignerNotGranted, document.Authority, document.Generation, signer, document.OwnershipDomain))
			continue
		}
		live = append(live, liveVerifiedAuthority{document: document, signer: signer})
	}
	return live, refusals, nil
}

// withdrawnPresence is the presence bindings whose NEWEST delivered generation
// is a tombstone. The registry reads it when the applied state cannot be read,
// so a withdrawal delivery has already made is honoured even then. Decoded, not
// re-verified, for the reason withdrawnBindings gives.
func (a *SolutionAuthorityActivation) withdrawnPresence(ctx context.Context) (map[string]bool, error) {
	records, err := a.newestDelivered(ctx, SolutionDeliveryPresence)
	if err != nil {
		return nil, err
	}
	removed := map[string]bool{}
	for _, record := range records {
		document, err := solutionhost.PresenceFromVerified(record.Payload)
		if err != nil {
			return nil, fmt.Errorf("read delivered presence %s generation %d: %w", record.DocumentID, record.Generation, err)
		}
		if document.Removed {
			removed[document.Binding] = true
		}
	}
	return removed, nil
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
