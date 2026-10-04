package business

import (
	"context"
	"fmt"
	"time"
)

// Solution registrations are projections of reconciled declarations. Delivery
// owns their presence and removal; runtime processes cannot write them.

// SolutionRegistrationStatus is derived from the record at read time.
type SolutionRegistrationStatus string

const (
	SolutionRegistrationActive       SolutionRegistrationStatus = "active"
	SolutionRegistrationPending      SolutionRegistrationStatus = "pending"
	SolutionRegistrationIncompatible SolutionRegistrationStatus = "incompatible"
	SolutionRegistrationTombstoned   SolutionRegistrationStatus = "tombstoned"
)

// SolutionFrontendHalf is a stored frontend observation. The frontend validates
// the manifest and its runtime compatibility when reading the projection.
type SolutionFrontendHalf struct {
	Revision        int64
	Manifest        string
	ContractVersion string
}

// SolutionBackendHalf is the stored backend registration.
type SolutionBackendHalf struct {
	Revision        int64
	Upstream        string
	ServiceAlias    string
	ContractVersion string
}

// SolutionDeclaredBinding is the declaration that produced this record: the
// SolutionHostBinding delivery handed the host, and the generation of it the
// host applied (solution_host_bindings.go, issue #952).
type SolutionDeclaredBinding struct {
	BindingID  string
	Generation uint64
	// Release is publisher/name@version of the applied generation. It is the
	// declared release.
	Release string
	// TargetID is the immutable solution target this declaration opened — the
	// identity an installation names, carried here so a consumer asked about a
	// route alias can resolve it to a target and compare identities rather than
	// strings.
	TargetID string
}

// SolutionRegistration is the canonical record for one solution.
type SolutionRegistration struct {
	SolutionID string
	Publisher  string
	Revision   int64
	Frontend   *SolutionFrontendHalf
	Backend    *SolutionBackendHalf
	// Declared is the binding that declared this record, or nil when the record
	// has not been reconciled from a declaration.
	Declared     *SolutionDeclaredBinding
	UpdatedAt    time.Time
	TombstonedAt *time.Time
}

// Status reports declared availability, independent of a runtime clock.
func (r *SolutionRegistration) Status() SolutionRegistrationStatus {
	switch {
	case r.TombstonedAt != nil:
		return SolutionRegistrationTombstoned
	case r.Frontend != nil && r.Backend != nil &&
		r.Frontend.ContractVersion != "" && r.Backend.ContractVersion != "" &&
		r.Frontend.ContractVersion != r.Backend.ContractVersion:
		return SolutionRegistrationIncompatible
	case r.Frontend == nil || r.Backend == nil:
		return SolutionRegistrationPending
	default:
		return SolutionRegistrationActive
	}
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
