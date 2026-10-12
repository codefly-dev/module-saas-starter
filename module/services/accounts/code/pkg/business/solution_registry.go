package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Solution registrations are projections of reconciled declarations. Delivery
// owns their presence and removal; runtime processes cannot write them.

var (
	// ErrSolutionRegistrationNotFound is returned when no record exists for the
	// solution a caller named.
	//
	// It survives the deletion of the runtime registration writer because it
	// describes a READ, not a write: the Work Context mint looks a solution's
	// boundary seed up by the id a verified credential named, and "there is no
	// such registration" is one of the answers it must be able to give. See
	// mapSolutionBoundaryError.
	ErrSolutionRegistrationNotFound = errors.New("solution registration not found")

	// ErrSolutionRegistrationTombstoned is returned when the record exists and
	// is withdrawn.
	//
	// Distinct from NotFound on purpose, and more load-bearing now than it was:
	// the cold cutover WITHDRAWS a runtime-registered row rather than deleting
	// it, so "exists but is not authorized" is a state the registry holds
	// durably and a mint must refuse by name rather than reporting an absence.
	ErrSolutionRegistrationTombstoned = errors.New("solution registration is tombstoned")
)

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

// SolutionDeclaredKind is what a declaration declares the presence of. Core
// requires it on every presence document and refuses any other value
// (solutionhost.Kind), so every admitted declaration has exactly one of these
// two and the host never has to infer it.
//
// There is no third value and no zero value with a meaning. A record whose kind
// is the empty string is a record this host cannot route, and both surfaces
// refuse it by name rather than falling back to the kind that happens to be more
// permissive — the module surface, which carries no per-viewer admission.
type SolutionDeclaredKind string

const (
	// SolutionDeclaredKindSolution is a composed solution instance, routed at
	// /solutions/<alias>/* behind per-viewer installation admission.
	SolutionDeclaredKindSolution SolutionDeclaredKind = "solution"
	// SolutionDeclaredKindModule is one module instance, routed at /v1/<alias>/*
	// through the ordinary authenticated pipeline.
	SolutionDeclaredKindModule SolutionDeclaredKind = "module"
)

// Valid reports whether a kind is one this host routes. Checked where a
// declaration becomes a record, so an unknown kind is a refusal naming the
// binding rather than a row no surface will serve.
func (kind SolutionDeclaredKind) Valid() bool {
	return kind == SolutionDeclaredKindSolution || kind == SolutionDeclaredKindModule
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
	// Kind is what the declaration declared the presence of, copied from the
	// applied document. It decides which routing surface serves this record.
	Kind SolutionDeclaredKind
}

// SolutionRegistration is the canonical record for one solution.
//
// It carries NO runtime-boundary seed. There used to be a `RuntimeBoundary`
// field holding the stored per-registration random (issue #1015); migration 28
// drops the column, and the boundary is derived from Declared.BindingID per
// organization instead (SolutionRuntimeBoundary). A field no store can populate
// would read as "this solution has no boundary" at every call site that found it
// empty, which is the opposite of the truth.
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
// SolutionBoundarySeed is what the mint reads about one declared solution.
//
// BindingID replaced an opaque `Seed` read off the registration row: the
// boundary is derived from the declared presence binding, not from a
// per-registration random.
//
// BackendServing is DELIVERED presence, never a heartbeat or a lease. The
// branch deleted the renewal path, so a row is serving when its applied
// declared generation carries a backend half; MissingBackendHalf says so for
// the refusal, which names the two columns it read rather than saying only
// "not serving".
type SolutionBoundarySeed struct {
	BindingID          string
	Publisher          string
	BackendServing     bool
	MissingBackendHalf string
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
// solutionBoundaryNamespace is the fixed namespace the BINDING ID is hashed
// under to produce a per-binding namespace. It is a constant of this host, not
// a secret: unguessability comes from the org derivation below plus the fact
// that a boundary is never returned on a readable record, not from hiding this.
var solutionBoundaryNamespace = uuid.MustParse("6f1b1f3e-7c4a-5c2b-9e55-1a2b3c4d5e6f")

// SolutionRuntimeBoundary derives one organization's boundary for a solution
// from the DECLARED PRESENCE BINDING ID.
//
// It used to take the `runtime_boundary` column, a per-registration
// `gen_random_uuid()` default. That was wrong in a way nothing caught: the
// column is per REGISTRATION, so a solution withdrawn and re-registered took a
// fresh random and every run filed under the old boundary was orphaned, while a
// binding that never moved could still have its boundary replaced by a write it
// did not make. The binding id is the identity that survives re-registration
// and is terminal with its tombstone, which is exactly the lifetime a boundary
// must have.
//
// The binding id is NOT a UUID — a binding may carry characters a path segment
// may not — so it is hashed under this host's namespace to get one, and the org
// derivation then runs unchanged. Two steps, because one boundary per solution
// would make every tenant of a solution share one.
//
// Rotation is coarse by construction: the binding id is the only input, so a
// boundary moves exactly when the binding does. SOLUTION_REGISTRATION.md §6
// states that cost.
func SolutionRuntimeBoundary(bindingID, orgID string) (string, error) {
	if strings.TrimSpace(bindingID) == "" {
		return "", errors.New("solution runtime boundary needs a declared binding id")
	}
	if orgID == "" {
		return "", errors.New("solution runtime boundary needs an organization")
	}
	namespace := uuid.NewSHA1(solutionBoundaryNamespace, []byte(bindingID))
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
//
// WHICH HALVES ARE REQUIRED DEPENDS ON THE KIND, and that is not a relaxation.
// A solution serves a page and a backend, so both halves and agreeing contract
// versions are what make it usable. A module has no browser remote at all: it is
// reached at /v1/<alias>/*, nothing loads a manifest for it, and demanding a
// frontend half would leave every module PENDING for ever — serving traffic the
// gateway refuses while the registry reports it as waiting for a deployment that
// is not coming. The incompatible case needs two contract versions to disagree,
// so it cannot arise for a module either.
//
// A record with no declaration keeps the solution reading. It is the stricter of
// the two, and an undeclared record is withdrawn by the cutover's own constraint
// anyway, so this is the fail-closed direction.
func (r *SolutionRegistration) Status() SolutionRegistrationStatus {
	if r.TombstonedAt != nil {
		return SolutionRegistrationTombstoned
	}
	if r.Declared != nil && r.Declared.Kind == SolutionDeclaredKindModule {
		if r.Backend == nil {
			return SolutionRegistrationPending
		}
		return SolutionRegistrationActive
	}
	switch {
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
