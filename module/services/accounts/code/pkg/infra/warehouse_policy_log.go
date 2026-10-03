package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"
)

// The external policy log, as the deployment's warehouse presents it.
//
// SHAPE, from the infrastructure that provisions it: a DAY-partitioned
// `policy_log` table clustered on (operation_id, subject_id) with
// deletion protection, plus a `policy_log_replay` view that dedupes by
// ROW_NUMBER() over operation_id ordered by sequence. Writer and reader are
// distinct roles, and **the reader deliberately holds no job-creation
// permission** — so a replay is a bounded row read with a restriction, never a
// query. That constraint is why `Entries` below takes a sequence and a limit
// rather than a predicate: there is no query planner on the read path to give a
// predicate to.
//
// WHAT IS NOT HERE. The concrete warehouse client is not implemented in this
// commit, and the reason is the one that governed the keyless verifier too: a
// client that cannot be exercised against the real service before shipping, in
// the service that owns authority, is worth less than an honest absence. The
// protocol above it is complete and tested; what is missing is the transport.
//
// So this file provides the SEAM and one real implementation of it — an
// in-memory log used by tests and by a single-process local run — and
// `UnavailablePolicyLog`, which is what a deployment gets until the warehouse
// client lands. `UnavailablePolicyLog` is not a stub that pretends: it refuses
// every append, which makes a host configured for a policy log refuse to narrow
// authority rather than narrow it unwitnessed.

// ErrPolicyLogNotProvisioned reports that this deployment has no policy log
// transport.
var ErrPolicyLogNotProvisioned = errors.New("policy log transport is not provisioned on this deployment")

// UnavailablePolicyLog refuses everything, by name.
//
// It is the correct behaviour for a deployment whose warehouse client has not
// landed, and it is deliberately not a no-op that returns a fabricated receipt.
// A fabricated receipt would be the tautology this whole protocol exists to
// prevent: the host would record that the log witnessed a narrowing, nothing
// would have, and a restore would silently undo it with the receipt still
// sitting there as evidence that it had not.
type UnavailablePolicyLog struct{}

// Append refuses. A host wired with this cannot narrow authority, which
// `WithPolicyLoggedNarrowing` reports as `ErrPolicyLogUnreachable`.
func (UnavailablePolicyLog) Append(
	context.Context, *business.PolicyLogEntry,
) (*business.PolicyLogReceipt, error) {
	return nil, fmt.Errorf("%w: no warehouse client is wired, so a narrowing cannot be witnessed",
		ErrPolicyLogNotProvisioned)
}

// Entries refuses, which keeps the host's staleness window closing and so stops
// it serving. That is the intended consequence: a host that cannot read the log
// does not know whether its authority is current.
func (UnavailablePolicyLog) Entries(
	context.Context, uint64, int,
) ([]*business.PolicyLogRecord, error) {
	return nil, fmt.Errorf("%w: no warehouse client is wired, so the log cannot be read",
		ErrPolicyLogNotProvisioned)
}

var _ business.PolicyLog = UnavailablePolicyLog{}
