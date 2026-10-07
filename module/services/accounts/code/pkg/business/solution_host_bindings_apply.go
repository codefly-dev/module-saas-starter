package business

import (
	"context"
	"fmt"
	"time"

	"github.com/codefly-dev/core/solutionhost"
)

// Reconciling an admitted generation into the durable registry (issue #952).
//
// The registry is a projection of admitted declarations, with a registry-wide
// revision sequence and tombstones. Runtime processes cannot write presence.
//
// Every apply is one transaction over one binding, and it re-decides under the
// row lock rather than trusting the verdict the pass computed from a snapshot.
// That is what makes two replicas converge without applying a generation twice:
// both read the same mount and both propose the same generation, and the second
// to take the lock finds it already applied and writes nothing.

// ListSolutionHostBindings returns every durable binding record, ordered by
// binding ID. One host holds tens of bindings at most, so the reconciler reads
// the whole set on every pass rather than tailing changes.
func (s *Service) ListSolutionHostBindings(ctx context.Context) ([]*SolutionHostBindingRecord, error) {
	var records []*SolutionHostBindingRecord
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		records, err = s.store.ListSolutionHostBindings(ctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list solution host bindings: %w", err)
	}
	return records, nil
}

// recordSolutionHostBindingDesired records what delivery is showing this host for
// one binding, and why it is not applied when it is not.
//
// It writes desired state only. A binding whose document was withheld keeps the
// generation it had applied, which is the whole point of preparing before
// activating: the last generation that passed every check goes on serving, and
// what delivery wants plus the reason it was refused sit beside it.
func (s *Service) recordSolutionHostBindingDesired(
	ctx context.Context, document *solutionhost.SolutionHostBinding,
	withheldReason string, settled bool, now time.Time,
) error {
	digest, err := document.Digest()
	if err != nil {
		return err
	}
	canonical, err := solutionhost.Marshal(document)
	if err != nil {
		return err
	}
	desired := SolutionHostBindingGeneration{
		Generation: document.Generation,
		Digest:     digest,
		Document:   string(canonical),
		At:         now,
	}
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		record, err := s.store.GetSolutionHostBindingForUpdate(ctx, document.Binding)
		if err != nil {
			return err
		}
		if record == nil {
			record = &SolutionHostBindingRecord{BindingID: document.Binding}
		}
		record.HostCoordinate = document.Host.Coordinate
		record.HostComponent = document.Host.Component
		record.Desired = &desired
		// A withheld document's reason is recorded here. An admitted one's is
		// NOT cleared here unless the generation is already applied (settled):
		// the apply that follows either clears it or replaces it, and clearing
		// it first would reset pending_since on every pass — so a refusal stuck
		// for a day would read as one that started seconds ago, which is the
		// opposite of what this field is for.
		if withheldReason != "" || settled {
			applyPendingReason(record, withheldReason, now)
		}
		record.UpdatedAt = now
		if err := s.store.SaveSolutionHostBinding(ctx, record); err != nil {
			return err
		}
		if withheldReason == "" {
			return nil
		}
		return s.store.RecordSolutionGenerationDecision(ctx, &SolutionGenerationDecision{
			BindingID:  document.Binding,
			Generation: document.Generation,
			Digest:     digest,
			Decision:   SolutionGenerationRefused,
			Reason:     withheldReason,
			Release:    document.Release.Identity(),
			DecidedAt:  now,
		})
	})
}

// recordSolutionHostBindingRefusal records a reason against a binding without
// touching desired or applied state. It is what an apply that failed, and a
// document this host could not even parse, leave behind.
func (s *Service) recordSolutionHostBindingRefusal(
	ctx context.Context, bindingID, reason string, now time.Time,
) error {
	if reason == "" {
		return nil
	}
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		record, err := s.store.GetSolutionHostBindingForUpdate(ctx, bindingID)
		if err != nil {
			return err
		}
		if record == nil {
			// Nothing has ever been delivered for this binding that this host
			// could read, so there is no record to hang a reason on. The caller
			// still reports it.
			return ErrSolutionHostBindingNotDeclared
		}
		record.PendingReason = reason
		record.PendingSince = pendingSince(record, now)
		record.UpdatedAt = now
		if err := s.store.SaveSolutionHostBinding(ctx, record); err != nil {
			return err
		}
		// The trail keeps a superseded refusal, which the binding row cannot:
		// pending_reason is overwritten by the next pass's answer.
		if record.Desired != nil {
			return s.store.RecordSolutionGenerationDecision(ctx, &SolutionGenerationDecision{
				BindingID:  bindingID,
				Generation: record.Desired.Generation,
				Digest:     record.Desired.Digest,
				Decision:   SolutionGenerationRefused,
				Reason:     reason,
				DecidedAt:  now,
			})
		}
		return nil
	})
}

// applyPendingReason sets or clears a binding's blocking reason, keeping the
// instant the CURRENT reason first appeared so an operator can tell a refusal
// that has just started from one that has been stuck for a day. Every pass
// re-records a standing refusal, so the instant has to survive a re-record —
// replacing it each time would make a permanently stuck binding look fresh.
func applyPendingReason(record *SolutionHostBindingRecord, reason string, now time.Time) {
	if reason == "" {
		record.PendingReason = ""
		record.PendingSince = nil
		return
	}
	record.PendingSince = pendingSince(record, now)
	record.PendingReason = reason
}

func pendingSince(record *SolutionHostBindingRecord, now time.Time) *time.Time {
	if record.PendingSince != nil {
		return record.PendingSince
	}
	since := now
	return &since
}

// applySolutionHostBinding reconciles one admitted generation. It is the pass's,
// not a service operation: reconciling one binding without the set-wide admission
// that preceded it would apply a generation core never approved.
//
// It re-runs core's admission against the LOCKED record rather than the
// snapshot the pass judged, because another replica may have applied this exact
// generation in between. core answers DecisionCurrent for that, which is not an
// error and writes nothing — only a rewrite of an applied generation is.
func (s *Service) applySolutionHostBinding(
	ctx context.Context, delivered *solutionhost.Delivered,
	coordinate string, domains []string, domainsBySigner map[string][]string, now time.Time,
) error {
	// Re-derived here rather than taken from the pass: apply owns its own
	// read of the attested bytes, so there is no second value a caller could
	// have adjusted between admission and the write.
	document, err := delivered.Document()
	if err != nil {
		return err
	}
	digest, err := document.Digest()
	if err != nil {
		return err
	}
	canonical, err := solutionhost.Marshal(document)
	if err != nil {
		return err
	}
	// Resolved before the transaction opens: a present generation this host
	// cannot address is refused without taking a lock, and the refusal names the
	// route rather than a database error.
	var solutionID string
	if !document.Removed {
		if solutionID, err = solutionHostBindingRegistryKey(document); err != nil {
			return err
		}
	}

	// A withdrawal CLOSES the installable target and revokes every active
	// installation naming it. That is a narrowing of authority, so it must be
	// witnessed by the external policy log before it takes effect, and the
	// append has to happen BEFORE this transaction opens — a slow log must not
	// hold database locks. Whether a close is coming is only knowable from the
	// live target, so it is read here, unlocked; the locked read inside the
	// transaction is still the one that decides.
	//
	// The two reads can disagree, and only one direction is dangerous. If this
	// one saw a live target and the locked one finds none, another replica
	// closed it first: the appended entry is still true and commits. If this
	// one saw none and the locked one finds one, a close would run with nothing
	// appended for it — so reconcileSolutionTarget REFUSES an unwitnessed
	// close, and the next pass, which now reads a live target, carries it.
	var witnessedTargetClose string
	apply := func(ctx context.Context) error {
		record, err := s.store.GetSolutionHostBindingForUpdate(ctx, document.Binding)
		if err != nil {
			return err
		}
		if record == nil {
			record = &SolutionHostBindingRecord{BindingID: document.Binding}
		}
		// core judges this one document against this one binding's applied
		// state. The set-wide rules — alias collisions, a binding declared
		// twice — were settled by the pass, and the partial unique index on the
		// registry key is the durable backstop for two replicas racing.
		host := solutionhost.Host{
			Coordinate:      coordinate,
			Domains:         domains,
			DomainsBySigner: domainsBySigner,
			Reserved:        []string{SolutionHostReservedRouteNamespace},
		}
		if state, ok := record.AppliedState(); ok {
			host.Applied = []solutionhost.Applied{state}
		}
		// The VERIFIED value, not the document read out of it. core's Admit
		// takes *Delivered precisely so a second judgement cannot be made on
		// bytes whose attestation was checked once and then dropped on the way
		// here.
		admissions, err := host.Admit(delivered)
		if err != nil {
			// One document, so core's set-level error and this document's own
			// refusal are the same thing; returning it is what leaves the
			// running generation untouched.
			return err
		}
		if admissions[0].Decision == solutionhost.DecisionCurrent {
			// Another replica applied it, or this pass raced its own previous
			// one. Nothing to do, and deliberately not an error — but it is
			// recorded, because "delivery kept saying the same thing" and
			// "delivery stopped" are different facts and the append is
			// idempotent on (binding, generation, digest, decision), so a poll
			// does not grow the trail.
			return s.store.RecordSolutionGenerationDecision(ctx, &SolutionGenerationDecision{
				BindingID:  document.Binding,
				Generation: document.Generation,
				Digest:     digest,
				Decision:   SolutionGenerationCurrent,
				Release:    document.Release.Identity(),
				DecidedAt:  now,
			})
		}

		// The registry revision this apply produced. It is recorded on the
		// generation-history row so the trail says which registry state the
		// generation became, rather than carrying a zero that reads like a
		// revision nobody issued. A tombstone's withdrawal produces one too.
		var appliedRegistryRevision *int64

		previous := record.Applied
		// The identity an organisation installs moves with this transaction, so a
		// reused alias cannot carry an installation across to another binding
		// (solution_targets.go).
		targetID, err := s.reconcileSolutionTarget(ctx, document, solutionID, witnessedTargetClose, now)
		if err != nil {
			return err
		}
		if document.Removed {
			withdrawn, err := s.withdrawDeclaredSolutionRegistration(ctx, record, document, targetID, now)
			if err != nil {
				return err
			}
			if withdrawn > 0 {
				appliedRegistryRevision = &withdrawn
			}
		} else {
			if previous != nil && !previous.Removed &&
				previous.SolutionID != "" && previous.SolutionID != solutionID {
				// The generation moved this binding's route. The alias it held
				// is withdrawn in the same transaction that claims the new one,
				// so there is no instant at which both are served.
				if _, err := s.withdrawDeclaredSolutionRegistration(ctx, record, document, targetID, now); err != nil {
					return err
				}
			}
			produced, err := s.declareSolutionRegistration(ctx, document, solutionID, targetID, now)
			if err != nil {
				return err
			}
			appliedRegistryRevision = &produced
		}

		record.HostCoordinate = document.Host.Coordinate
		record.HostComponent = document.Host.Component
		generation := SolutionHostBindingGeneration{
			Generation: document.Generation,
			Digest:     digest,
			Document:   string(canonical),
			At:         now,
		}
		record.Desired = &generation
		// The half core owns is built by core. AppliedFrom is defined as "the
		// record a host persists after applying a document", so deriving those
		// fields here would be a second copy of its rules — and the copy is what
		// drifts: presence v2 made the ownership domain a required part of this
		// record, and a host that assembled it by hand fed core an applied state
		// with no domain, which core refuses. The symptom was not a widened
		// check. It was a reconciler that applied its first generation and then
		// refused the WHOLE set on every pass afterwards, because one unusable
		// applied record makes the set unjudgeable.
		//
		// Reading it from core instead means the next required field is a
		// compile error or a loud refusal here, not a silent omission.
		owned, err := solutionhost.AppliedFrom(document)
		if err != nil {
			return fmt.Errorf("build applied record for binding %q: %w", document.Binding, err)
		}
		record.Applied = &SolutionHostBindingApplied{
			SolutionHostBindingGeneration: generation,
			Removed:                       owned.Removed,
			Routes:                        owned.Routes,
			Domain:                        owned.Domain,
			// Release is the host's own column: core's Applied does not carry it.
			// It identifies the release whose presence this generation declares.
			Release: document.Release.Identity(),
		}
		if document.Removed {
			// A tombstone keeps the key it withdrew, so the removal stays
			// attributable to the record it removed and a later binding
			// claiming that alias is not blocked by it.
			if previous != nil {
				record.Applied.SolutionID = previous.SolutionID
			}
		} else {
			record.Applied.SolutionID = solutionID
		}
		applyPendingReason(record, "", now)
		record.UpdatedAt = now
		if err := s.store.SaveSolutionHostBinding(ctx, record); err != nil {
			return err
		}
		decided := SolutionGenerationApplied
		if document.Removed {
			decided = SolutionGenerationTombstoned
		}
		// The target reconciliation above returns the identity it decided on,
		// INCLUDING a tombstone's closed one. Re-reading the live target here
		// is what dropped it: by this point the close has already committed in
		// this transaction, so the read finds nothing and the row that records
		// a withdrawal loses the identity it withdrew.
		if err := s.store.RecordSolutionGenerationDecision(ctx, &SolutionGenerationDecision{
			BindingID:        document.Binding,
			TargetID:         targetID,
			Generation:       document.Generation,
			Digest:           digest,
			Decision:         decided,
			Release:          record.Applied.Release,
			RegistryRevision: appliedRegistryRevision,
			DecidedAt:        now,
		}); err != nil {
			return err
		}
		return s.emitTx(ctx, solutionHostBindingActor(record), "system",
			EventSolutionHostBindingApplied, "solution", solutionHostBindingResource(record), "",
			map[string]any{
				"binding_id":  document.Binding,
				"solution_id": record.Applied.SolutionID,
				"generation":  int64(document.Generation),
				"digest":      digest,
				"release":     record.Applied.Release,
				"removed":     document.Removed,
				"routes":      record.Applied.Routes,
			})
	}

	// A withdrawal: find the target this binding has live, and run the whole
	// transaction under the policy log when there is one to close.
	if document.Removed {
		live, err := s.liveSolutionTargetForBinding(ctx, document.Binding)
		if err != nil {
			return err
		}
		if live != nil {
			witnessedTargetClose = newPolicyLogOperationID("close-solution-target")
			return s.WithPolicyLoggedNarrowing(ctx, closedSolutionTargetPolicyLogEntry(
				witnessedTargetClose, live.ID, document.Binding, live.SolutionID, document.Generation,
			), apply)
		}
	}
	return s.store.WithControlPlane(ctx, apply)
}

// liveSolutionTargetForBinding is the unlocked read that decides whether a
// withdrawal has a target to close, so the policy log append can happen before
// the apply transaction opens.
//
// It filters the whole target listing rather than taking a lock, because the
// locked read inside the transaction is the one that decides and a second lock
// here would only widen the window it holds. The listing is one row per
// solution instance this host has ever presented, which is the same set the
// operator surface already pages through.
func (s *Service) liveSolutionTargetForBinding(ctx context.Context, bindingID string) (*SolutionTarget, error) {
	var live *SolutionTarget
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		targets, err := s.store.ListSolutionTargets(ctx)
		if err != nil {
			return err
		}
		for _, target := range targets {
			if target.BindingID == bindingID && target.Live() {
				live = target
				return nil
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return live, nil
}

// declareSolutionRegistration records the binding and immutable target that own
// this alias. Only observations for that same declared target may survive.
func (s *Service) declareSolutionRegistration(
	ctx context.Context, document *solutionhost.SolutionHostBinding, solutionID, targetID string, now time.Time,
) (int64, error) {
	current, err := s.store.GetSolutionRegistrationForUpdate(ctx, solutionID)
	if err != nil {
		return 0, err
	}
	if current != nil && current.Declared != nil &&
		current.Declared.BindingID != document.Binding && current.TombstonedAt == nil {
		// A live record another binding declared. core refuses this before a
		// generation applies; reaching it means two replicas judged different
		// snapshots, and refusing here is what stops the second from stealing
		// the route. A TOMBSTONED declared record may be taken over: its
		// declaration withdrew the alias, and the binding that held it keeps
		// its own tombstone generation.
		return 0, fmt.Errorf("%w: %q is declared by binding %q",
			ErrSolutionHostBindingRouteHeld, solutionID, current.Declared.BindingID)
	}
	revision, err := s.store.NextSolutionRegistryRevision(ctx)
	if err != nil {
		return 0, err
	}
	kind := SolutionDeclaredKind(document.Kind)
	if !kind.Valid() {
		// Core refuses any kind outside its own two before a document is
		// admitted, so reaching this means core's vocabulary grew and this host
		// has no surface for the new value. Refusing names the binding and leaves
		// the generation it had applied running; writing the record anyway would
		// produce a row no routing surface serves and no operator can explain.
		return 0, fmt.Errorf("%w: binding %q declares kind %q",
			ErrSolutionHostBindingKindNotRoutable, document.Binding, document.Kind)
	}
	declared := &SolutionDeclaredBinding{
		BindingID:  document.Binding,
		Generation: document.Generation,
		Release:    document.Release.Identity(),
		TargetID:   targetID,
		Kind:       kind,
	}
	next := &SolutionRegistration{
		SolutionID: solutionID,
		Publisher:  solutionRegistrationPublisher(solutionID),
		Revision:   revision,
		UpdatedAt:  now,
		Declared:   declared,
	}
	if current != nil && current.Declared != nil && current.Declared.TargetID == targetID {
		next.Publisher = current.Publisher
		// A tombstone cleared the halves; a live record keeps the ones it has.
		next.Frontend = current.Frontend
		next.Backend = current.Backend
	}
	if err := s.store.SaveSolutionRegistration(ctx, next); err != nil {
		return 0, err
	}
	return revision, nil
}

// withdrawDeclaredSolutionRegistration tombstones the registry record a binding
// holds, in the transaction that applies the generation withdrawing it.
//
// The tombstone retains the declaration that withdrew the alias.
// It returns the registry revision the withdrawal produced, or 0 when there was
// nothing to withdraw, so the generation history records the registry state this
// generation actually became.
func (s *Service) withdrawDeclaredSolutionRegistration(
	ctx context.Context, record *SolutionHostBindingRecord,
	document *solutionhost.SolutionHostBinding, closedTargetID string, now time.Time,
) (int64, error) {
	if record.Applied == nil || record.Applied.SolutionID == "" {
		// A removal for a binding this host never applied. The generation is
		// still recorded, so a later document at a lower generation is refused;
		// there is simply no registry record to withdraw.
		return 0, nil
	}
	solutionID := record.Applied.SolutionID
	current, err := s.store.GetSolutionRegistrationForUpdate(ctx, solutionID)
	if err != nil {
		return 0, err
	}
	if current == nil {
		return 0, nil
	}
	if current.Declared != nil && current.Declared.BindingID != document.Binding {
		// Another binding has taken the alias over since. Withdrawing it here
		// would remove presence this binding no longer owns.
		return 0, nil
	}
	if current.TombstonedAt != nil {
		return 0, nil
	}
	revision, err := s.store.NextSolutionRegistryRevision(ctx)
	if err != nil {
		return 0, err
	}
	tombstoned := now
	next := &SolutionRegistration{
		SolutionID:   current.SolutionID,
		Publisher:    current.Publisher,
		Revision:     revision,
		UpdatedAt:    now,
		TombstonedAt: &tombstoned,
		Declared: &SolutionDeclaredBinding{
			BindingID:  document.Binding,
			Generation: document.Generation,
			Release:    document.Release.Identity(),
			// The target the withdrawal closed, kept on the tombstone: the
			// record stays attributable to the identity whose consent ended.
			TargetID: closedTargetID,
		},
	}
	if err := s.store.SaveSolutionRegistration(ctx, next); err != nil {
		return 0, err
	}
	if err := s.emitTx(ctx, "solution:"+solutionID, "system",
		EventSolutionRegistrationDeleted, "solution", solutionID, "",
		map[string]any{
			"solution_id": solutionID,
			"publisher":   current.Publisher,
			"revision":    revision,
		}); err != nil {
		return 0, err
	}
	return revision, nil
}

// solutionRegistrationPublisher is the owner of record for a solution id: the
// canonical identity used to attribute its declared presence.
func solutionRegistrationPublisher(solutionID string) string { return "solution:" + solutionID }

// solutionHostBindingActor and solutionHostBindingResource name a declared
// binding in the audit trail by the solution it is a deployment of when it has
// one, and by its binding ID when it does not — a generation refused before it
// ever addressed a registry record has no solution id to name.
func solutionHostBindingActor(record *SolutionHostBindingRecord) string {
	return "solution:" + solutionHostBindingResource(record)
}

func solutionHostBindingResource(record *SolutionHostBindingRecord) string {
	if record.Applied != nil && record.Applied.SolutionID != "" {
		return record.Applied.SolutionID
	}
	return record.BindingID
}

// reconcileSolutionTarget keeps the installable identity in step with the
// generation being applied, in the same transaction.
//
// A present generation for a binding with no live target OPENS one: this is the
// start of a period an organisation may consent to. A present generation for a
// binding that already has one keeps that identity and only moves its alias — the
// identity surviving a rename is the point, because an installation named it. A
// tombstone CLOSES it, and the row survives: a closed target is the evidence that
// consent ended, and deleting it would make a reused alias indistinguishable from
// a continuous presence.
//
// A tombstone for a binding with no live target is a no-op, like the registry
// withdrawal beside it: there is no period to end.
//
// Closing a target REVOKES every active installation naming it, in this same
// transaction. The target identity makes inheritance inexpressible — a
// replacement binding has its own target, so it cannot be reached through a
// predecessor's installation — but an installation left ACTIVE on a closed
// target is still a record of consent to a presence that has ended, and every
// reader would have to remember to ask about the target's liveness to avoid
// honouring it. Revoking here means only one place knows the rule.
//
// It returns the closed target so the caller can attribute the generation
// history and the audit event to the identity that ended. Reading it back after
// the close would find nothing, which is how the tombstone's history row lost
// its target.
// witnessedTargetClose is the policy log operation whose append landed for the
// close this call is about to perform, and "" when none did. A close with no
// witness is REFUSED: closing the target revokes every installation naming it,
// and performing that with nothing appended is the unwitnessed narrowing the
// protocol forbids outright.
func (s *Service) reconcileSolutionTarget(
	ctx context.Context, document *solutionhost.SolutionHostBinding,
	solutionID, witnessedTargetClose string, now time.Time,
) (string, error) {
	live, err := s.store.GetLiveSolutionTargetForUpdate(ctx, document.Binding)
	if err != nil {
		return "", err
	}
	if document.Removed {
		if live == nil {
			return "", nil
		}
		if witnessedTargetClose == "" {
			return "", fmt.Errorf(
				"%w: closing solution target %s was not appended to the policy log, "+
					"so the installations it revokes would be unwitnessed",
				ErrPolicyLogUnreachable, live.ID)
		}
		if err := s.store.CloseSolutionTarget(ctx, live.ID, document.Generation, now); err != nil {
			return "", err
		}
		if err := s.revokeInstallationsOfClosedTarget(ctx, live, document.Generation, now); err != nil {
			return "", err
		}
		return live.ID, nil
	}
	if live == nil {
		opened, err := s.store.OpenSolutionTarget(ctx, document.Binding, solutionID, document.Generation, now)
		if err != nil {
			return "", err
		}
		return opened.ID, nil
	}
	if live.SolutionID == solutionID {
		return live.ID, nil
	}
	if err := s.store.RetargetSolutionTarget(ctx, live.ID, solutionID, now); err != nil {
		return "", err
	}
	return live.ID, nil
}

// revokeInstallationsOfClosedTarget ends the consent a withdrawn presence was
// granted, and records why.
//
// The reason is stored on the row because a revoked installation that reads like
// an administrator's uninstall is a different fact from one the platform ended:
// the first is somebody's decision and the second is something they must be told
// about, and re-installing is only available for one of them.
//
// Each revocation emits the installation-revoked event with the target it named,
// so the trail says which presence ended rather than only that an installation
// stopped.
func (s *Service) revokeInstallationsOfClosedTarget(
	ctx context.Context, target *SolutionTarget, generation uint64, now time.Time,
) error {
	reason := fmt.Sprintf(
		"solution target %s was withdrawn by generation %d of binding %s",
		target.ID, generation, target.BindingID)
	revoked, err := s.store.RevokeInstallationsOfTarget(ctx, target.ID, reason, now)
	if err != nil {
		return err
	}
	for _, installation := range revoked {
		if err := s.emitTx(ctx, "system", "system", EventInstallationRevoked,
			"installation", installation.InstallationID, installation.OrgID,
			map[string]any{
				"installation_id": installation.InstallationID,
				"target_id":       target.ID,
				"binding_id":      target.BindingID,
				"generation":      int64(generation),
				"revoked_reason":  reason,
			}); err != nil {
			return err
		}
	}
	return nil
}

// ListSolutionTargets returns every installable identity, open and closed. An
// operator reads it to tell a solution that is still the one an organisation
// installed from one that merely reuses its route.
func (s *Service) ListSolutionTargets(ctx context.Context) ([]*SolutionTarget, error) {
	var targets []*SolutionTarget
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		targets, err = s.store.ListSolutionTargets(ctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list solution targets: %w", err)
	}
	return targets, nil
}

// ListSolutionGenerationHistory returns one component's decision trail, newest
// first. It is the Catalogue's history panel and the only record of a refusal the
// next pass superseded.
func (s *Service) ListSolutionGenerationHistory(
	ctx context.Context, bindingID string, limit int,
) ([]*SolutionGenerationDecision, error) {
	var history []*SolutionGenerationDecision
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		history, err = s.store.ListSolutionGenerationHistory(ctx, bindingID, limit)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list solution generation history: %w", err)
	}
	return history, nil
}
