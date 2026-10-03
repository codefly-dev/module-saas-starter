package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

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

// MemoryPolicyLog is an append-only log in this process.
//
// It is a real implementation of the interface, not a mock: it enforces
// idempotency on the operation id and monotonic sequencing, which are the two
// properties the protocol leans on. What it cannot provide is the property that
// matters most — being EXTERNAL to the database it witnesses — so it is for
// tests and single-process local runs, and a deployment using it would have a
// log that is restored alongside the state it is meant to witness.
type MemoryPolicyLog struct {
	mu       sync.Mutex
	records  []*business.PolicyLogRecord
	byID     map[string]*business.PolicyLogReceipt
	sequence uint64
	now      func() time.Time
}

// NewMemoryPolicyLog builds the in-process log.
func NewMemoryPolicyLog(now func() time.Time) *MemoryPolicyLog {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryPolicyLog{byID: map[string]*business.PolicyLogReceipt{}, now: now}
}

// Append records an entry, idempotently on its operation id.
//
// The idempotency is the log's responsibility rather than the host's, and that
// placement is deliberate: a host that checked "did I already append?" before
// appending has a window between the two calls in which a concurrent attempt
// appends a second entry for the same operation — and a log with two entries for
// one revocation cannot be replayed into a single answer.
func (m *MemoryPolicyLog) Append(
	_ context.Context, entry *business.PolicyLogEntry,
) (*business.PolicyLogReceipt, error) {
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.byID[entry.OperationID]; ok {
		return existing, nil
	}
	m.sequence++
	receipt := &business.PolicyLogReceipt{
		Receipt:    fmt.Sprintf("mem-%d-%s", m.sequence, entry.OperationID),
		Sequence:   m.sequence,
		AppendedAt: m.now(),
	}
	m.byID[entry.OperationID] = receipt
	// The entry is cloned: the caller keeps its own, and a log whose records
	// could be mutated through a retained pointer would be a log that could be
	// rewritten after the fact.
	clone := *entry
	clone.Policy = clonePolicy(entry.Policy)
	m.records = append(m.records, &business.PolicyLogRecord{
		PolicyLogEntry: clone,
		Receipt:        receipt.Receipt,
		Sequence:       receipt.Sequence,
		LoggedAt:       receipt.AppendedAt,
	})
	return receipt, nil
}

// Entries returns records after a sequence, in order, bounded by limit.
func (m *MemoryPolicyLog) Entries(
	_ context.Context, after uint64, limit int,
) ([]*business.PolicyLogRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*business.PolicyLogRecord, 0, len(m.records))
	for _, record := range m.records {
		if record.Sequence > after {
			clone := *record
			clone.Policy = clonePolicy(record.Policy)
			out = append(out, &clone)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// clonePolicy deep-copies a policy payload through JSON.
//
// Through JSON rather than by walking the map: the payload is arbitrary nested
// data the log round-trips as JSON anyway, so copying it any other way would
// preserve shapes the real log would not — and a test passing on a shape the
// warehouse cannot store is a test that proves nothing about the warehouse.
func clonePolicy(policy map[string]any) map[string]any {
	if policy == nil {
		return nil
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil
	}
	return clone
}

var (
	_ business.PolicyLog = UnavailablePolicyLog{}
	_ business.PolicyLog = (*MemoryPolicyLog)(nil)
)
