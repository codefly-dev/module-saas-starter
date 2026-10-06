package business

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
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
//
// RuntimeBoundary is the opaque id every Work Context minted for this solution
// is sealed under (issue #1015). The store assigns it when the record is
// created and no path here ever writes it again, which is what makes it the
// host's and not the solution's: see SolutionRuntimeBoundaryStore.
type SolutionRegistration struct {
	SolutionID string
	Publisher  string
	Revision   int64
	// RuntimeBoundary is assigned by the database and never written from here.
	// It survives this branch's deletion of the runtime registration writer by
	// living in the save statement's omissions rather than in any planner; see
	// PostgresStore.SaveSolutionRegistration.
	RuntimeBoundary string
	Frontend        *SolutionFrontendHalf
	Backend         *SolutionBackendHalf
	// Declared is the binding that declared this record, or nil when the record
	// has not been reconciled from a declaration.
	Declared     *SolutionDeclaredBinding
	UpdatedAt    time.Time
	TombstonedAt *time.Time
}

// SolutionRuntimeBoundarySeedStore reads the runtime-boundary seeds the Work
// Context issuer needs. Both reads are their own rather than fields of the
// registry snapshot because the two have opposite audiences: the snapshot is
// the whole registry, which the gateway and the frontend cache, while a seed is
// the one thing that must never leave this host at all.
type SolutionRuntimeBoundarySeedStore interface {
	// SolutionRuntimeBoundarySeed returns one solution's seed, by the id a
	// verified credential named, together with the publisher that owns the
	// registration and whether its backend half is currently serving. The mint
	// checks all three.
	SolutionRuntimeBoundarySeed(ctx context.Context, solutionID string) (SolutionBoundarySeed, error)
	// SolutionRuntimeBoundarySeeds returns every stored seed, tombstones
	// included, for the collision check below. There are tens of registrations
	// in a deployment, so this is one small indexed read rather than a cache
	// that could answer with a seed the registry has already replaced.
	SolutionRuntimeBoundarySeeds(ctx context.Context) ([]string, error)
}

// SolutionBoundarySeed is what the mint reads about one registration: the seed
// its boundary is derived from, the publisher of record, and whether the half
// that mints is currently serving.
type SolutionBoundarySeed struct {
	Seed           string
	Publisher      string
	BackendServing bool
}

// SolutionRuntimeBoundary derives the boundary a solution's Work Context is
// sealed under, for one organization.
//
// It is derived rather than stored so that one tenant's boundary is not
// another's: a run is filed under (tenant, boundary), and a single
// per-solution value would make every tenant of a solution share one. A UUIDv5
// over the seed and the org id is stable for as long as the registration lives,
// unguessable without the seed — which never leaves this host — and needs no
// second table to stay consistent with the registration it belongs to.
//
// Rotation is deliberately coarse: the seed is the only input, so replacing it
// moves every organization's boundary at once and orphans whatever is still
// executing under the old one. SOLUTION_REGISTRATION.md §6 states that cost.
func SolutionRuntimeBoundary(seed, orgID string) (string, error) {
	namespace, err := uuid.Parse(seed)
	if err != nil {
		return "", fmt.Errorf("solution runtime boundary seed is not a UUID: %w", err)
	}
	if orgID == "" {
		return "", errors.New("solution runtime boundary needs an organization")
	}
	return uuid.NewSHA1(namespace, []byte(orgID)).String(), nil
}

// IsSolutionRuntimeBoundary reports whether a caller-named task_id is any
// registered solution's boundary — the seed itself, or the boundary derived
// from it for orgID.
//
// This is the check that makes a stable boundary safe (issue #1015). A boundary
// is not a secret in practice: a consumer that reads one of its own runs can
// see the task it was admitted under, so one leaked read would otherwise let
// any caller holding a viewer's bearer mint an ORDINARY context naming it and
// reach that solution's runs. Refusing the collision is what keeps the only way
// to obtain a solution's boundary the credential that proves which solution is
// asking.
//
// Only orgID's derivation is checked, not every organization's: a capability is
// sealed with the tenant it was minted in, and a consumer scopes a run by
// (tenant, boundary), so naming another tenant's boundary yields a context that
// reaches nothing. Tombstoned registrations are included — a removed solution's
// runs may still be executing.
func IsSolutionRuntimeBoundary(seeds []string, candidate, orgID string) bool {
	if candidate == "" {
		return false
	}
	for _, seed := range seeds {
		if seed == "" {
			continue
		}
		if strings.EqualFold(seed, candidate) {
			return true
		}
		// A seed that does not parse cannot have produced a boundary, so there
		// is nothing it could collide with; the column is a uuid, so this is
		// unreachable short of a hand-edited row.
		boundary, err := SolutionRuntimeBoundary(seed, orgID)
		if err != nil {
			continue
		}
		if strings.EqualFold(boundary, candidate) {
			return true
		}
	}
	return false
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
