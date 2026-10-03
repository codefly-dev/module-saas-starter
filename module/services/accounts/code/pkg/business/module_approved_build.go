package business

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// The approved build, and the monotonicity contract core states but cannot
// enforce.
//
// Core's `SealSource.ApprovedBuild` documents the rule and says plainly that it
// compares values for EQUALITY and cannot detect a source that moves backwards —
// so keeping it is the implementer's job, which means this host's. The rule has
// two clauses and the second is the one that is easy to miss:
//
//  1. for one principal, the incarnation never DECREASES;
//  2. a change of DIGEST comes with an INCREASE in the incarnation.
//
// Core's own `MemorySealSource` missed the second: approve B at incarnation 5,
// then approve A again at incarnation 5, and every capability sealed to A at 5 —
// which the move to B revoked — verifies again. A swap-back at a fixed counter
// is the rewind the rule exists to prevent, reached through the field the rule
// did not cover.
//
// WHY A GUARD RATHER THAN CARE. The rule is a property of a sequence of writes,
// so no single write can be checked against the document it came from — only
// against what this host last served. A reviewer cannot verify care, and the
// next person to add a write path does not read this comment. So the guard
// refuses the write.
//
// WHERE THE VALUES COME FROM. The approved digest is the authority document's,
// which is signed and delivered out of band — the one input to the execution
// check that the caller has no influence over. This type does not fetch it; it
// holds what the delivery pipeline resolved, and enforces the ordering rule over
// it.

// ErrApprovedBuildRewind reports a write that would move a principal's approved
// build backwards.
//
// A source that genuinely must rewind is reconstructing state rather than
// recording it, and belongs behind a fresh instance — which is core's own
// guidance and the reason this is a refusal rather than a clamp. Clamping would
// silently serve a value the writer did not intend, and the writer would never
// learn.
var ErrApprovedBuildRewind = errors.New("approved build would move backwards for this principal")

// ApprovedBuildRecord is one principal's approved build.
type ApprovedBuildRecord struct {
	Digest      ApprovedDigest
	Incarnation uint64
}

// MonotonicApprovedBuilds enforces the contract over whatever it is given.
//
// It is deliberately NOT a cache in front of a store: a cache would serve a
// stale answer when the store moved forward, and the whole point of the
// incarnation is that a capability sealed at an older one stops verifying. It
// holds the authoritative in-process view and refuses writes that violate the
// ordering.
type MonotonicApprovedBuilds struct {
	mutex   sync.RWMutex
	records map[string]ApprovedBuildRecord
	// known separates "this principal bears no approved build" from "this
	// principal is unknown". Without it the two are the same missing map entry,
	// and collapsing them is the dangerous direction: an unknown principal
	// would mint an unbound capability, which is a capability for an identity
	// the host has never heard of.
	known map[string]bool
}

// NewMonotonicApprovedBuilds returns an empty authority.
//
// Empty means every principal is UNKNOWN, not that every principal bears no
// build — so a host that has resolved nothing refuses rather than minting
// unbound capabilities for everyone.
func NewMonotonicApprovedBuilds() *MonotonicApprovedBuilds {
	return &MonotonicApprovedBuilds{
		records: map[string]ApprovedBuildRecord{},
		known:   map[string]bool{},
	}
}

// Declare records that a principal exists, bearing no approved build yet.
//
// Separate from Approve because the two are different facts. A principal
// declared but not approved is a known identity that may act unbound if the
// capability it asks for permits it; an undeclared one is nothing at all.
func (m *MonotonicApprovedBuilds) Declare(principalID string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.known[principalID] = true
}

// Approve records a principal's approved build, enforcing both clauses.
//
// The digest is compared as a DIGEST rather than as a string, so the same
// content written as a bare `sha256:…` and as a full reference is one digest and
// not a change — otherwise a re-delivery that merely respelled the reference
// would read as a new image and demand an incarnation bump it has no reason to
// have.
func (m *MonotonicApprovedBuilds) Approve(principalID string, record ApprovedBuildRecord) error {
	if principalID == "" {
		return fmt.Errorf("%w: no principal named", ErrApprovedBuildRewind)
	}
	if _, ok := digestPortion(string(record.Digest)); !ok {
		// An approval is a statement about immutable content, so a value that
		// is not a digest cannot be one. Refused here as well as at the
		// comparison, because a non-digest stored now is a non-digest served to
		// every later check.
		return fmt.Errorf("%w: %q is not a digest, and only immutable content can be approved",
			ErrApprovedBuildRewind, record.Digest)
	}
	if record.Incarnation == 0 {
		// Core's schema requires `gte=1` when the pair is present, so a zero
		// incarnation would produce a seal its own validation refuses — and it
		// would do so at mint time, far from here.
		return fmt.Errorf("%w: incarnation must be at least 1", ErrApprovedBuildRewind)
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	previous, had := m.records[principalID]
	if had {
		// Clause 1: never decreases.
		if record.Incarnation < previous.Incarnation {
			return fmt.Errorf("%w: incarnation %d is below the %d already served for %s",
				ErrApprovedBuildRewind, record.Incarnation, previous.Incarnation, principalID)
		}
		// Clause 2, the one MemorySealSource missed: a change of digest must
		// come with an INCREASE. Equal incarnations with different digests is
		// exactly the swap-back, and it is the case a check on clause 1 alone
		// lets through.
		if record.Incarnation == previous.Incarnation &&
			!digestsEqual(string(record.Digest), string(previous.Digest)) {
			return fmt.Errorf(
				"%w: %s moves from %s to %s without advancing incarnation %d — "+
					"every capability sealed to the first digest at that incarnation would verify again",
				ErrApprovedBuildRewind, principalID,
				previous.Digest, record.Digest, record.Incarnation)
		}
	}

	m.records[principalID] = record
	m.known[principalID] = true
	return nil
}

// ApprovedBuild answers what this host approves for a principal.
//
// Three states, each a different answer:
//
//   - a record → the approved digest and its incarnation;
//   - known, no record → ErrNoApprovedBuild. The principal may act unbound.
//   - unknown → ErrUnknownExecutionPrincipal. Refused.
//
// It takes NO attributes of the workload, which is core's rule and the reason it
// holds: resolving by (service account, image digest) would answer "approved"
// for whatever a pod presents, which lets a superseded generation in. The issuer
// answers what IT approves; the caller's running image is established
// independently; a mismatch is a refusal.
func (m *MonotonicApprovedBuilds) ApprovedBuild(
	_ context.Context, principalID string,
) (ApprovedDigest, uint64, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	record, had := m.records[principalID]
	if had {
		return record.Digest, record.Incarnation, nil
	}
	if m.known[principalID] {
		return "", 0, ErrNoApprovedBuild
	}
	return "", 0, ErrUnknownExecutionPrincipal
}

var _ ExecutionAuthority = (*MonotonicApprovedBuilds)(nil)
