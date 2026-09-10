package business

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Durable solution registry (issue #534).
//
// A solution registers two halves that arrive as separate requests from
// separate processes: the frontend manifest the host renders, and the upstream
// the gateway proxies to. There is no cross-process transaction available to
// make the pair atomic, so this service does not pretend to one. It stores each
// half as it arrives, under one record and one revision line, and serves the
// registration only once both halves are present, compatible, and live. A
// half-registered solution is a durable PENDING record — never a page whose
// backend does not exist.
//
// Revisions come from a database sequence, so they order writes across records
// as well as within one. Compare-and-swap on that revision is what stops a
// publisher still holding an older view from overwriting newer state; a
// tombstone at a revision the caller does not hold is what stops a retiring
// deployment's last retry from resurrecting a registration an operator removed.

var (
	// ErrSolutionRegistrationNotFound is returned when no record exists for the
	// solution id — distinct from a tombstone, which is a record.
	ErrSolutionRegistrationNotFound = errors.New("solution registration not found")
	// ErrSolutionRegistrationStale is returned when the caller's
	// expected_revision does not match the record's current revision.
	ErrSolutionRegistrationStale = errors.New("solution registration revision is stale")
	// ErrSolutionRegistrationRevisionRequired is returned when a caller tries to
	// change a half that already holds different content without naming the
	// revision it believes it is replacing.
	ErrSolutionRegistrationRevisionRequired = errors.New("solution registration revision required to replace an existing half")
	// ErrSolutionRegistrationTombstoned is returned when a write lands on a
	// deregistered record without naming the tombstone's revision. This is the
	// refusal a delayed heartbeat from a retired deployment gets.
	ErrSolutionRegistrationTombstoned = errors.New("solution registration is tombstoned")
	// ErrSolutionPublisherMismatch is returned when a registration names a
	// different publisher than the one that owns the solution id.
	ErrSolutionPublisherMismatch = errors.New("solution registration is owned by another publisher")
	// ErrSolutionRegistrationHalfMissing is returned when a write names neither
	// half.
	ErrSolutionRegistrationHalfMissing = errors.New("solution registration must carry exactly one half")
)

// SolutionRegistrationStatus is derived from the record at read time.
type SolutionRegistrationStatus string

const (
	SolutionRegistrationActive       SolutionRegistrationStatus = "active"
	SolutionRegistrationPending      SolutionRegistrationStatus = "pending"
	SolutionRegistrationExpired      SolutionRegistrationStatus = "expired"
	SolutionRegistrationIncompatible SolutionRegistrationStatus = "incompatible"
	SolutionRegistrationTombstoned   SolutionRegistrationStatus = "tombstoned"
)

// SolutionFrontendHalf is the stored frontend registration. Manifest is the
// document the frontend validated; nothing here parses it.
type SolutionFrontendHalf struct {
	Revision        int64
	Manifest        string
	ContractVersion string
	LeaseExpiresAt  time.Time
}

// SolutionBackendHalf is the stored backend registration.
type SolutionBackendHalf struct {
	Revision        int64
	Upstream        string
	ServiceAlias    string
	ContractVersion string
	LeaseExpiresAt  time.Time
}

// SolutionRegistration is the canonical record for one solution.
type SolutionRegistration struct {
	SolutionID   string
	Publisher    string
	Revision     int64
	Frontend     *SolutionFrontendHalf
	Backend      *SolutionBackendHalf
	UpdatedAt    time.Time
	TombstonedAt *time.Time
}

// Status resolves the record against the wall clock.
//
// The order is deliberate. A tombstone outranks everything: the record was
// removed, and nothing about its former endpoints is interesting. A contract
// mismatch outranks an expired lease because it is a property of the record
// itself — renewing the lease would not fix it — whereas expiry is a transient
// liveness fact that the publisher's next heartbeat clears.
func (r *SolutionRegistration) Status(now time.Time) SolutionRegistrationStatus {
	switch {
	case r.TombstonedAt != nil:
		return SolutionRegistrationTombstoned
	case r.Frontend == nil || r.Backend == nil:
		return SolutionRegistrationPending
	case r.Frontend.ContractVersion != "" && r.Backend.ContractVersion != "" &&
		r.Frontend.ContractVersion != r.Backend.ContractVersion:
		return SolutionRegistrationIncompatible
	case !r.Frontend.LeaseExpiresAt.After(now) || !r.Backend.LeaseExpiresAt.After(now):
		return SolutionRegistrationExpired
	default:
		return SolutionRegistrationActive
	}
}

// SolutionFrontendRegistration is the caller-supplied frontend half.
type SolutionFrontendRegistration struct {
	Manifest        string
	ContractVersion string
}

// SolutionBackendRegistration is the caller-supplied backend half.
type SolutionBackendRegistration struct {
	Upstream        string
	ServiceAlias    string
	ContractVersion string
}

// SolutionRegistrationWrite is one half-write. Exactly one of Frontend and
// Backend is set; ExpectedRevision carries the compare-and-swap token when the
// caller holds one.
type SolutionRegistrationWrite struct {
	SolutionID       string
	Publisher        string
	ExpectedRevision *int64
	Lease            time.Duration
	Frontend         *SolutionFrontendRegistration
	Backend          *SolutionBackendRegistration
}

// PutSolutionRegistration writes or renews one half of one registration.
//
// A write whose content matches what is stored is a lease renewal: it refreshes
// liveness and deliberately leaves the revision alone, so a heartbeat is
// invisible to a consumer comparing snapshots and a heartbeat storm cannot
// churn every replica's cache. Claiming a half the record does not yet hold
// needs no token. Replacing a half that holds different content does, and it
// must be the record's current revision.
func (s *Service) PutSolutionRegistration(ctx context.Context, write SolutionRegistrationWrite) (*SolutionRegistration, error) {
	if (write.Frontend == nil) == (write.Backend == nil) {
		return nil, ErrSolutionRegistrationHalfMissing
	}
	if write.SolutionID == "" || write.Publisher == "" {
		return nil, ErrSolutionRegistrationHalfMissing
	}

	var result *SolutionRegistration
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		now := time.Now().UTC()
		current, err := s.store.GetSolutionRegistrationForUpdate(ctx, write.SolutionID)
		if err != nil {
			return err
		}
		next, changed, err := planSolutionRegistrationWrite(current, write, now)
		if err != nil {
			return err
		}
		if changed {
			revision, err := s.store.NextSolutionRegistryRevision(ctx)
			if err != nil {
				return err
			}
			next.Revision = revision
			if write.Frontend != nil {
				next.Frontend.Revision = revision
			} else {
				next.Backend.Revision = revision
			}
		}
		if err := s.store.SaveSolutionRegistration(ctx, next); err != nil {
			return err
		}
		// A renewal is not an event: it says the publisher is still alive, which
		// the lease already records. Auditing only real changes keeps the trail
		// readable at heartbeat cadence.
		if changed {
			half := "backend"
			if write.Frontend != nil {
				half = "frontend"
			}
			if err := s.emitTx(ctx, "solution:"+write.SolutionID, "system",
				EventSolutionRegistrationUpdated, "solution", write.SolutionID, "",
				map[string]any{
					"solution_id": write.SolutionID,
					"publisher":   write.Publisher,
					"half":        half,
					"revision":    next.Revision,
				}); err != nil {
				return err
			}
		}
		result = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// planSolutionRegistrationWrite computes the record a write produces, and
// whether it advanced the registration (as opposed to renewing a lease). It is
// pure so the compare-and-swap rules can be exercised without a database.
func planSolutionRegistrationWrite(
	current *SolutionRegistration, write SolutionRegistrationWrite, now time.Time,
) (*SolutionRegistration, bool, error) {
	leaseUntil := now.Add(write.Lease)

	if current == nil {
		// Nothing to compare against: a caller presenting a token is working
		// from a view of a record that no longer exists.
		if write.ExpectedRevision != nil {
			return nil, false, ErrSolutionRegistrationStale
		}
		next := &SolutionRegistration{
			SolutionID: write.SolutionID,
			Publisher:  write.Publisher,
			UpdatedAt:  now,
		}
		applySolutionHalf(next, write, leaseUntil)
		return next, true, nil
	}

	if current.Publisher != write.Publisher {
		return nil, false, ErrSolutionPublisherMismatch
	}
	if write.ExpectedRevision != nil && *write.ExpectedRevision != current.Revision {
		return nil, false, ErrSolutionRegistrationStale
	}

	if current.TombstonedAt != nil {
		// Re-registering a removed solution is allowed, but only for a caller
		// that has seen the tombstone and named its revision. A retry still
		// carrying the pre-deletion view — or none at all — is refused.
		if write.ExpectedRevision == nil {
			return nil, false, ErrSolutionRegistrationTombstoned
		}
		next := &SolutionRegistration{
			SolutionID: current.SolutionID,
			Publisher:  current.Publisher,
			UpdatedAt:  now,
		}
		applySolutionHalf(next, write, leaseUntil)
		return next, true, nil
	}

	next := *current
	next.UpdatedAt = now
	if unchangedSolutionHalf(current, write) {
		renewSolutionHalf(&next, write, leaseUntil)
		return &next, false, nil
	}
	if solutionHalfPresent(current, write) && write.ExpectedRevision == nil {
		return nil, false, ErrSolutionRegistrationRevisionRequired
	}
	applySolutionHalf(&next, write, leaseUntil)
	return &next, true, nil
}

func solutionHalfPresent(record *SolutionRegistration, write SolutionRegistrationWrite) bool {
	if write.Frontend != nil {
		return record.Frontend != nil
	}
	return record.Backend != nil
}

func unchangedSolutionHalf(record *SolutionRegistration, write SolutionRegistrationWrite) bool {
	if write.Frontend != nil {
		return record.Frontend != nil &&
			record.Frontend.Manifest == write.Frontend.Manifest &&
			record.Frontend.ContractVersion == write.Frontend.ContractVersion
	}
	return record.Backend != nil &&
		record.Backend.Upstream == write.Backend.Upstream &&
		record.Backend.ServiceAlias == write.Backend.ServiceAlias &&
		record.Backend.ContractVersion == write.Backend.ContractVersion
}

func renewSolutionHalf(record *SolutionRegistration, write SolutionRegistrationWrite, leaseUntil time.Time) {
	if write.Frontend != nil {
		renewed := *record.Frontend
		renewed.LeaseExpiresAt = leaseUntil
		record.Frontend = &renewed
		return
	}
	renewed := *record.Backend
	renewed.LeaseExpiresAt = leaseUntil
	record.Backend = &renewed
}

func applySolutionHalf(record *SolutionRegistration, write SolutionRegistrationWrite, leaseUntil time.Time) {
	if write.Frontend != nil {
		record.Frontend = &SolutionFrontendHalf{
			Manifest:        write.Frontend.Manifest,
			ContractVersion: write.Frontend.ContractVersion,
			LeaseExpiresAt:  leaseUntil,
		}
		return
	}
	record.Backend = &SolutionBackendHalf{
		Upstream:        write.Backend.Upstream,
		ServiceAlias:    write.Backend.ServiceAlias,
		ContractVersion: write.Backend.ContractVersion,
		LeaseExpiresAt:  leaseUntil,
	}
}

// DeleteSolutionRegistration deregisters a solution. The record survives as a
// tombstone with both halves cleared: routing and page availability go away in
// the same write that records the removal, and the tombstone is what a later
// heartbeat collides with instead of recreating the registration.
//
// Deleting an already-tombstoned record is a no-op that returns the tombstone,
// so a retried deregistration is safe.
func (s *Service) DeleteSolutionRegistration(
	ctx context.Context, solutionID string, expectedRevision *int64,
) (*SolutionRegistration, error) {
	var result *SolutionRegistration
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		now := time.Now().UTC()
		current, err := s.store.GetSolutionRegistrationForUpdate(ctx, solutionID)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrSolutionRegistrationNotFound
		}
		if expectedRevision != nil && *expectedRevision != current.Revision {
			return ErrSolutionRegistrationStale
		}
		if current.TombstonedAt != nil {
			result = current
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
		}
		if err := s.store.SaveSolutionRegistration(ctx, next); err != nil {
			return err
		}
		if err := s.emitTx(ctx, "solution:"+solutionID, "system",
			EventSolutionRegistrationDeleted, "solution", solutionID, "",
			map[string]any{
				"solution_id": solutionID,
				"publisher":   current.Publisher,
				"revision":    revision,
			}); err != nil {
			return err
		}
		result = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListSolutionRegistrations returns the registry snapshot a consumer rebuilds
// its cache from, plus the highest revision in it: two consumers reporting the
// same registry revision have converged.
func (s *Service) ListSolutionRegistrations(
	ctx context.Context, includeTombstoned bool,
) ([]*SolutionRegistration, int64, error) {
	var (
		records  []*SolutionRegistration
		revision int64
	)
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		records, revision, err = s.store.ListSolutionRegistrations(ctx, includeTombstoned)
		return err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list solution registrations: %w", err)
	}
	return records, revision, nil
}
