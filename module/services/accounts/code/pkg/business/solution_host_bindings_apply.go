package business

import (
	"context"
	"fmt"
	"time"

	"github.com/codefly-dev/core/solutionhost"
)

// Reconciling an admitted generation into the durable registry (issue #952).
//
// The registry is already durable, with a registry-wide revision sequence,
// leases, tombstones and compare-and-swap. This is not a second store: an
// applied generation writes the same solution_registrations record a
// self-registering runtime writes, and the only thing it adds to that record is
// the declaration that produced it.
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
	ctx context.Context, document *solutionhost.SolutionHostBinding,
	coordinate string, domains []string, now time.Time,
) error {
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

	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
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
			Coordinate: coordinate,
			Domains:    domains,
			Reserved:   []string{SolutionHostReservedRouteNamespace},
		}
		if state, ok := record.AppliedState(); ok {
			host.Applied = []solutionhost.Applied{state}
		}
		admissions, err := host.Admit(document)
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

		previous := record.Applied
		// The identity an organisation installs moves with this transaction, so a
		// reused alias cannot carry an installation across to another binding
		// (solution_targets.go).
		if err := s.reconcileSolutionTarget(ctx, document, solutionID, now); err != nil {
			return err
		}
		if document.Removed {
			if err := s.withdrawDeclaredSolutionRegistration(ctx, record, document, now); err != nil {
				return err
			}
		} else {
			if previous != nil && !previous.Removed &&
				previous.SolutionID != "" && previous.SolutionID != solutionID {
				// The generation moved this binding's route. The alias it held
				// is withdrawn in the same transaction that claims the new one,
				// so there is no instant at which both are served.
				if err := s.withdrawDeclaredSolutionRegistration(ctx, record, document, now); err != nil {
					return err
				}
			}
			if err := s.declareSolutionRegistration(ctx, document, solutionID, now); err != nil {
				return err
			}
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
		record.Applied = &SolutionHostBindingApplied{
			SolutionHostBindingGeneration: generation,
			Removed:                       document.Removed,
			Routes:                        document.Aliases(),
			Release:                       document.Release.Identity(),
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
		historyTarget := ""
		if live, err := s.store.GetLiveSolutionTargetForUpdate(ctx, document.Binding); err == nil && live != nil {
			historyTarget = live.ID
		}
		if err := s.store.RecordSolutionGenerationDecision(ctx, &SolutionGenerationDecision{
			BindingID:  document.Binding,
			TargetID:   historyTarget,
			Generation: document.Generation,
			Digest:     digest,
			Decision:   decided,
			Release:    record.Applied.Release,
			DecidedAt:  now,
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
	})
}

// declareSolutionRegistration writes declared presence into the registry record
// the alias addresses, creating it when nothing has registered under it yet.
//
// An existing UNDECLARED record is adopted rather than replaced: its halves and
// leases are observations a running solution reported, and discarding them would
// take a working solution offline at the instant its presence became declared.
// That adoption is the migration path — a solution that self-registers today
// becomes declared without a gap.
func (s *Service) declareSolutionRegistration(
	ctx context.Context, document *solutionhost.SolutionHostBinding, solutionID string, now time.Time,
) error {
	current, err := s.store.GetSolutionRegistrationForUpdate(ctx, solutionID)
	if err != nil {
		return err
	}
	if current != nil && current.Declared != nil &&
		current.Declared.BindingID != document.Binding && current.TombstonedAt == nil {
		// A live record another binding declared. core refuses this before a
		// generation applies; reaching it means two replicas judged different
		// snapshots, and refusing here is what stops the second from stealing
		// the route. A TOMBSTONED declared record may be taken over: its
		// declaration withdrew the alias, and the binding that held it keeps
		// its own tombstone generation.
		return fmt.Errorf("%w: %q is declared by binding %q",
			ErrSolutionHostBindingRouteHeld, solutionID, current.Declared.BindingID)
	}
	revision, err := s.store.NextSolutionRegistryRevision(ctx)
	if err != nil {
		return err
	}
	declared := &SolutionDeclaredBinding{
		BindingID:  document.Binding,
		Generation: document.Generation,
		Release:    document.Release.Identity(),
	}
	next := &SolutionRegistration{
		SolutionID: solutionID,
		// The publisher of record stays the identity this host mints for that
		// solution id, because it is the identity a heartbeat proves. Writing
		// the release publisher here instead would make every heartbeat for a
		// declared solution fail the publisher check, so declared presence
		// would never be observed and the record would sit pending forever.
		Publisher: solutionRegistrationPublisher(solutionID),
		Revision:  revision,
		UpdatedAt: now,
		Declared:  declared,
	}
	if current != nil {
		next.Publisher = current.Publisher
		// A tombstone cleared the halves; a live record keeps the ones it has.
		next.Frontend = current.Frontend
		next.Backend = current.Backend
	}
	return s.store.SaveSolutionRegistration(ctx, next)
}

// withdrawDeclaredSolutionRegistration tombstones the registry record a binding
// holds, in the transaction that applies the generation withdrawing it.
//
// The declaration is KEPT on the tombstone. That is what makes the removal hold:
// a heartbeat from the retiring runtime finds a declared tombstone and is
// refused, where an undeclared tombstone can be re-registered by a caller that
// names its revision.
func (s *Service) withdrawDeclaredSolutionRegistration(
	ctx context.Context, record *SolutionHostBindingRecord,
	document *solutionhost.SolutionHostBinding, now time.Time,
) error {
	if record.Applied == nil || record.Applied.SolutionID == "" {
		// A removal for a binding this host never applied. The generation is
		// still recorded, so a later document at a lower generation is refused;
		// there is simply no registry record to withdraw.
		return nil
	}
	solutionID := record.Applied.SolutionID
	current, err := s.store.GetSolutionRegistrationForUpdate(ctx, solutionID)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if current.Declared != nil && current.Declared.BindingID != document.Binding {
		// Another binding has taken the alias over since. Withdrawing it here
		// would remove presence this binding no longer owns.
		return nil
	}
	if current.TombstonedAt != nil {
		return nil
	}
	revision, err := s.store.NextSolutionRegistryRevision(ctx)
	if err != nil {
		return err
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
		},
	}
	if err := s.store.SaveSolutionRegistration(ctx, next); err != nil {
		return err
	}
	return s.emitTx(ctx, "solution:"+solutionID, "system",
		EventSolutionRegistrationDeleted, "solution", solutionID, "",
		map[string]any{
			"solution_id": solutionID,
			"publisher":   current.Publisher,
			"revision":    revision,
		})
}

// solutionRegistrationPublisher is the owner of record for a solution id: the
// subject this host's own registration credential proves (`sub=solution:<id>`).
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
func (s *Service) reconcileSolutionTarget(
	ctx context.Context, document *solutionhost.SolutionHostBinding, solutionID string, now time.Time,
) error {
	live, err := s.store.GetLiveSolutionTargetForUpdate(ctx, document.Binding)
	if err != nil {
		return err
	}
	if document.Removed {
		if live == nil {
			return nil
		}
		return s.store.CloseSolutionTarget(ctx, live.ID, document.Generation, now)
	}
	if live == nil {
		_, err := s.store.OpenSolutionTarget(ctx, document.Binding, solutionID, document.Generation, now)
		return err
	}
	if live.SolutionID == solutionID {
		return nil
	}
	return s.store.RetargetSolutionTarget(ctx, live.ID, solutionID, now)
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
