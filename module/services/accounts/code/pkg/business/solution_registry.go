package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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
	// ErrSolutionRegistrationIdentityRequired is returned when a write names no
	// solution id or no publisher. Distinct from the half rules: such a request
	// is not addressable at all, and reporting it as a half problem sends the
	// caller to inspect the wrong field.
	ErrSolutionRegistrationIdentityRequired = errors.New("solution registration requires a solution id and publisher")
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
// document the frontend validated, stored verbatim. The one part of it read
// here is the audit event types its dashboard graph declares, which the audit
// registry admits when the half is written (solution_audit_events.go).
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
//
// RuntimeBoundary is the opaque id every Work Context minted for this solution
// is sealed under (issue #1015). The store assigns it when the record is
// created and no path here ever writes it again, which is what makes it the
// host's and not the solution's: see SolutionRuntimeBoundaryStore.
type SolutionRegistration struct {
	SolutionID      string
	Publisher       string
	Revision        int64
	RuntimeBoundary string
	Frontend        *SolutionFrontendHalf
	Backend         *SolutionBackendHalf
	UpdatedAt       time.Time
	TombstonedAt    *time.Time
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

// Status resolves the record against the wall clock.
//
// The order is deliberate. A tombstone outranks everything: the record was
// removed, and nothing about its former endpoints is interesting. A contract
// mismatch outranks an expired lease because it is a property of the record
// itself — renewing the lease would not fix it — whereas expiry is a transient
// liveness fact that the publisher's next heartbeat clears.
//
// Expiry is evaluated over whichever halves are present, and outranks pending.
// A half that registered once and then stopped renewing is dead, not waiting:
// reporting it as pending sends an operator hunting the deployment that never
// arrived, when the one that did arrive is the thing that stopped.
func (r *SolutionRegistration) Status(now time.Time) SolutionRegistrationStatus {
	switch {
	case r.TombstonedAt != nil:
		return SolutionRegistrationTombstoned
	case r.Frontend != nil && r.Backend != nil &&
		r.Frontend.ContractVersion != "" && r.Backend.ContractVersion != "" &&
		r.Frontend.ContractVersion != r.Backend.ContractVersion:
		return SolutionRegistrationIncompatible
	case r.Frontend != nil && !r.Frontend.LeaseExpiresAt.After(now):
		return SolutionRegistrationExpired
	case r.Backend != nil && !r.Backend.LeaseExpiresAt.After(now):
		return SolutionRegistrationExpired
	case r.Frontend == nil || r.Backend == nil:
		return SolutionRegistrationPending
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
		return nil, ErrSolutionRegistrationIdentityRequired
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
		// The manifest's dashboard graph is also the solution's declaration of
		// the audit event types it owns (solution_audit_events.go). They are
		// admitted in this transaction, so a declaration the audit registry
		// refuses refuses the whole write and the last admitted registration
		// keeps serving. A renewal carries a manifest already admitted, and is
		// deliberately not re-read: a rule tightened by a later release must
		// not stop a working registration from renewing.
		var takenOver []string
		if changed && write.Frontend != nil {
			declared, err := ParseDeclaredAuditEventTypes(write.SolutionID, write.Frontend.Manifest)
			if err != nil {
				return err
			}
			if takenOver, err = s.admitDeclaredAuditEventTypes(ctx, write.SolutionID, declared); err != nil {
				return err
			}
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
			payload := map[string]any{
				"solution_id": write.SolutionID,
				"publisher":   write.Publisher,
				"half":        half,
				"revision":    next.Revision,
			}
			// A takeover moves every declared type in a namespace from the
			// solution the operator unbound to this one, so it is recorded with
			// the write that performed it.
			if len(takenOver) > 0 {
				payload["audit_namespaces_taken_over"] = takenOver
			}
			if err := s.emitTx(ctx, "solution:"+write.SolutionID, "system",
				EventSolutionRegistrationUpdated, "solution", write.SolutionID, "", payload); err != nil {
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
		// RuntimeBoundary is deliberately left empty: the store assigns it on
		// the INSERT this write becomes and reports back what it assigned, so
		// nothing above the database — including this planner — is ever in a
		// position to choose one.
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
		// The boundary is carried across the tombstone, not re-drawn: the
		// record is the same solution under the same publisher, and the runs it
		// already admitted stay the ones it can read.
		next := &SolutionRegistration{
			SolutionID:      current.SolutionID,
			Publisher:       current.Publisher,
			RuntimeBoundary: current.RuntimeBoundary,
			UpdatedAt:       now,
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
			sameManifest(record.Frontend.Manifest, write.Frontend.Manifest) &&
			record.Frontend.ContractVersion == write.Frontend.ContractVersion
	}
	return record.Backend != nil &&
		record.Backend.Upstream == write.Backend.Upstream &&
		record.Backend.ServiceAlias == write.Backend.ServiceAlias &&
		record.Backend.ContractVersion == write.Backend.ContractVersion
}

// sameManifest reports whether a stored frontend manifest and a re-sent one are
// the same JSON value.
//
// Comparing the bytes cannot tell: the registry stores the manifest as jsonb,
// which Postgres re-serializes on read — keys reordered, a space after every
// colon — so the stored text never equals the compact text the host sends.
// Compared byte for byte, every heartbeat of an unchanged solution read as a
// change: a new registry revision, a solution.registration_updated audit event
// and a registry cache invalidation on every replica, once per beat, forever.
// Two texts are the same manifest when they decode to equal values. Text that
// does not decode is compared as bytes, as before.
func sameManifest(stored, sent string) bool {
	if stored == sent {
		return true
	}
	storedValue, storedErr := decodeManifest(stored)
	sentValue, sentErr := decodeManifest(sent)
	if storedErr != nil || sentErr != nil {
		return false
	}
	return sameJSONValue(storedValue, sentValue)
}

func decodeManifest(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing data after the manifest")
	}
	return value, nil
}

// sameJSONValue compares two decoded manifests the way the column they live in
// does. reflect.DeepEqual cannot: json.Number holds the literal as written, so
// it compares spellings, and jsonb stores numbers as numeric — which never
// renders an exponent. A manifest carrying 1e-7 comes back as 0.0000001 and
// every heartbeat of it read as a change, which is the whole bug this function
// exists to stop, surviving for exactly the manifests whose numbers Postgres
// rewrites.
func sameJSONValue(stored, sent any) bool {
	switch left := stored.(type) {
	case map[string]any:
		right, ok := sent.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, present := right[key]
			if !present || !sameJSONValue(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := sent.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for index := range left {
			if !sameJSONValue(left[index], right[index]) {
				return false
			}
		}
		return true
	case json.Number:
		right, ok := sent.(json.Number)
		return ok && sameJSONNumber(left, right)
	case string:
		right, ok := sent.(string)
		return ok && left == right
	case bool:
		right, ok := sent.(bool)
		return ok && left == right
	case nil:
		return sent == nil
	default:
		return false
	}
}

// maxComparableNumberExponent bounds the exponent a number literal may carry
// before it is compared as text instead. A rational built from 1e999999999
// would be materialised digit by digit, and the literal is caller-supplied:
// the write that carries it is refused by the numeric column anyway, so
// falling back to a text comparison costs a renewal that was never going to
// land and spends no memory reaching that answer.
const maxComparableNumberExponent = 10000

// maxComparableNumberDigits bounds the mantissa for the same reason.
const maxComparableNumberDigits = 4096

// sameJSONNumber compares two JSON number literals by value, which is what
// jsonb's own equality does once they are numerics. big.Rat is exact over
// every JSON number — they are all finite decimals — so no change is lost to
// float rounding.
func sameJSONNumber(stored, sent json.Number) bool {
	if stored == sent {
		return true
	}
	if !comparableNumber(stored) || !comparableNumber(sent) {
		return false
	}
	left, leftOK := new(big.Rat).SetString(string(stored))
	right, rightOK := new(big.Rat).SetString(string(sent))
	if !leftOK || !rightOK {
		return false
	}
	return left.Cmp(right) == 0
}

func comparableNumber(literal json.Number) bool {
	text := string(literal)
	if len(text) > maxComparableNumberDigits {
		return false
	}
	exponent := strings.IndexAny(text, "eE")
	if exponent < 0 {
		return true
	}
	magnitude, err := strconv.Atoi(strings.TrimPrefix(text[exponent+1:], "+"))
	if err != nil {
		return false
	}
	if magnitude < 0 {
		magnitude = -magnitude
	}
	return magnitude <= maxComparableNumberExponent
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
			SolutionID:      current.SolutionID,
			Publisher:       current.Publisher,
			Revision:        revision,
			RuntimeBoundary: current.RuntimeBoundary,
			UpdatedAt:       now,
			TombstonedAt:    &tombstoned,
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
