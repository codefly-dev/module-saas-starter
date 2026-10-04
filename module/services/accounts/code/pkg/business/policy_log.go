package business

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The policy log: append → receipt → commit-with-receipt, before anything
// narrows.
//
// WHY IT IS EXTERNAL. A transactional audit event plus an append-only table in
// the same database is not an external record of authority. Restoring that
// database restores revoked authority together with its own history, so the
// history cannot witness against the state — the two were backed up together.
// The log therefore lives elsewhere, and what this host keeps is the RECEIPT:
// proof that an entry reached the log BEFORE the narrowing it describes took
// effect here.
//
// THE DIRECTION OF THE ASYMMETRY IS THE DESIGN. Two failures are possible
// between the two writes, and they must not be treated alike:
//
//   - append fails ⇒ nothing narrows. The caller is refused, and the state is
//     exactly as before. Safe, because authority was not reduced and nobody was
//     told it was.
//   - append succeeds, commit fails ⇒ the LOG says the narrowing happened and
//     this host has not applied it. That must fail CLOSED: every replica
//     enforces the narrowing, or refuses to serve, until the gap is reconciled.
//     The authority of record has spoken, and a host serving the wider authority
//     it still holds locally is serving authority that was revoked.
//
// The reverse — commit without append — must be IMPOSSIBLE, which is why the
// append comes first and its receipt is required by the commit.
//
// WHAT COUNTS AS A NARROWING. Revocation, a tenancy change, a narrowed envelope
// — and, deliberately, AUTHORISED REGRANTS too. A log that recorded only
// reductions could not answer "was this principal's authority restored, or has
// it never been taken away?", and those have different answers to "may it act
// now".

// PolicyLogSubjectKind is what a logged operation is about. The set is closed:
// an operation whose subject this host cannot name is one a reconciliation
// cannot act on, so there is no "other".
type PolicyLogSubjectKind string

const (
	PolicyLogPrincipal      PolicyLogSubjectKind = "principal"
	PolicyLogBinding        PolicyLogSubjectKind = "binding"
	PolicyLogInstallation   PolicyLogSubjectKind = "installation"
	PolicyLogTeamMembership PolicyLogSubjectKind = "team_membership"
	PolicyLogScopeGrant     PolicyLogSubjectKind = "scope_grant"
)

// Valid reports whether a kind is one a reconciliation can act on.
func (kind PolicyLogSubjectKind) Valid() bool {
	switch kind {
	case PolicyLogPrincipal, PolicyLogBinding, PolicyLogInstallation,
		PolicyLogTeamMembership, PolicyLogScopeGrant:
		return true
	}
	return false
}

// PolicyLogDecision is what the operation did to the subject's authority.
type PolicyLogDecision string

const (
	// PolicyLogNarrowed reduced it — a revocation, a tenancy change, a tighter
	// envelope.
	PolicyLogNarrowed PolicyLogDecision = "narrowed"
	// PolicyLogRegranted restored or widened it, by an authorised act.
	//
	// Logged for the same reason a narrowing is: without it the log cannot tell
	// "this authority was restored" from "it was never taken away", and those
	// have different answers to whether the subject may act now.
	PolicyLogRegranted PolicyLogDecision = "regranted"
)

// PolicyLogEntry is one operation offered to the log.
type PolicyLogEntry struct {
	// OperationID is the idempotency key, and it is the CALLER'S. A retry of the
	// same logical operation must append once however many times it is
	// attempted, and only the caller knows that two attempts are the same
	// operation. Generating it here would make every retry a new entry, and a
	// log with two entries for one revocation cannot be replayed into a single
	// answer.
	OperationID string

	Decision    PolicyLogDecision
	SubjectKind PolicyLogSubjectKind
	SubjectID   string

	// Policy is the authority as it stands AFTER the operation. The log holds
	// the resulting state rather than a diff, because a replay has to be able to
	// answer "what is this subject's authority" from one entry rather than by
	// folding every entry since the beginning.
	Policy map[string]any

	// Supersedes is the operation this one replaces, when it replaces one.
	Supersedes string
	// EnvelopeRevision is the ceiling the operation was decided against, so a
	// replay can tell an operation made under an older envelope from one made
	// under the current.
	EnvelopeRevision uint64

	// Actor is who performed it. Required: an authority change with no actor is
	// a change nobody can be asked about.
	Actor string
}

// Validate refuses an entry the log could not be replayed from.
func (entry *PolicyLogEntry) Validate() error {
	switch {
	case entry == nil:
		return errors.New("policy log entry is absent")
	case entry.OperationID == "":
		return errors.New("policy log entry has no operation id, so a retry of it could not be recognised as the same operation")
	case entry.Decision != PolicyLogNarrowed && entry.Decision != PolicyLogRegranted:
		return fmt.Errorf("policy log entry has decision %q, want %q or %q",
			entry.Decision, PolicyLogNarrowed, PolicyLogRegranted)
	case !entry.SubjectKind.Valid():
		return fmt.Errorf("policy log entry has subject kind %q, which no reconciliation could act on", entry.SubjectKind)
	case entry.SubjectID == "":
		return errors.New("policy log entry names no subject")
	case entry.Actor == "":
		return errors.New("policy log entry names no actor, so the authority change could not be attributed")
	}
	return nil
}

// PolicyLogReceipt is the log's acknowledgement of one appended entry.
type PolicyLogReceipt struct {
	// Receipt is the log's own token for the entry. Opaque to this host, and
	// stored verbatim: its presence is what separates "the log witnessed this"
	// from "this host decided it".
	Receipt string
	// Sequence is the log's monotonic position for the entry, which the host's
	// cursor is compared against.
	Sequence uint64
	// AppendedAt is when the log recorded it, by the LOG'S clock rather than
	// this host's. A host comparing its own clock against a log's ordering would
	// be comparing two unrelated things.
	AppendedAt time.Time
}

// ErrPolicyLogUnreachable reports that the log could not be appended to or read.
//
// It is the reason the host refuses to serve. Distinct from a refusal by the log
// because the two have opposite meanings: a log that said no has made a
// decision, and a log that could not be reached has made none — and a host that
// treated the second as the first would serve while believing it had checked.
var ErrPolicyLogUnreachable = errors.New("policy log is unreachable")

// ErrPolicyLogUnreconciled reports that the log holds an appended operation this
// host has not applied.
//
// This is the append-ok/commit-fail state, and it is a SERVING condition rather
// than an error on one request: the authority of record says a narrowing
// happened, so every replica must enforce it or stop serving until the gap is
// closed.
var ErrPolicyLogUnreconciled = errors.New("policy log holds an operation this host has not applied")

// PolicyLog is the external append-only authority record.
type PolicyLog interface {
	// Append records one entry and returns its receipt.
	//
	// It must be IDEMPOTENT on the entry's operation id: a retry returns the
	// original receipt rather than appending again. That requirement is on the
	// log rather than on this host because only the log can make it atomic —
	// a host checking "did I already append?" before appending has a window
	// between the two.
	Append(ctx context.Context, entry *PolicyLogEntry) (*PolicyLogReceipt, error)

	// Entries returns every entry after a sequence, in order.
	//
	// Used by reconciliation, never on a request path. The reader deliberately
	// holds no job-creation permission on the log's warehouse, so this is a
	// bounded row read with a restriction rather than a query.
	Entries(ctx context.Context, afterSequence uint64, limit int) ([]*PolicyLogRecord, error)
}

// PolicyLogRecord is one entry as the log returns it.
type PolicyLogRecord struct {
	PolicyLogEntry
	Receipt  string
	Sequence uint64
	LoggedAt time.Time
}

// PolicyLogStore is the host's local half: the receipts and the cursor.
type PolicyLogStore interface {
	// RecordPolicyLogAppend writes the receipt for an appended operation, with
	// no commit yet. Reports whether the row was new, so a retry can tell an
	// append it has already recorded from one it has not.
	RecordPolicyLogAppend(ctx context.Context, receipt *PolicyLogReceipt, entry *PolicyLogEntry) (bool, error)

	// CommitPolicyLogOperation marks an appended operation applied. It runs in
	// the SAME transaction as the narrowing it describes, which is what makes
	// "applied but not committed" impossible — the only reachable gap is
	// "appended but not applied", which fails closed.
	CommitPolicyLogOperation(ctx context.Context, operationID string, at time.Time) error

	// UncommittedPolicyLogOperations returns appended operations with no commit.
	UncommittedPolicyLogOperations(ctx context.Context) ([]*PolicyLogGap, error)

	// PolicyLogCursorState reads how far this host has reconciled, and when it
	// last reached the log.
	PolicyLogCursorState(ctx context.Context) (sequence uint64, reachedAt *time.Time, err error)

	// AdvancePolicyLogCursor records that the log was reached and read to a
	// sequence.
	AdvancePolicyLogCursor(ctx context.Context, sequence uint64, at time.Time) error
}

// PolicyLogGap is an appended operation this host has not applied.
type PolicyLogGap struct {
	OperationID string
	Sequence    uint64
	SubjectKind PolicyLogSubjectKind
	SubjectID   string
	AppendedAt  time.Time
}

// PolicyLogDeadline bounds how long an append may take.
//
// It exists because the alternative is lock accumulation, not slowness. The
// append happens before the narrowing's transaction opens — deliberately, so a
// slow log cannot hold database locks — but a caller blocked indefinitely on the
// log still holds its request, its connection and whatever the caller above it
// holds. A bounded wait converts that into a refusal the caller can act on.
//
// Five seconds: long enough that an ordinary warehouse append succeeds, short
// enough that an authority change fails visibly rather than hanging. A narrowing
// that cannot be logged must not proceed, so the refusal is the correct outcome
// and not a degraded one.
const PolicyLogDeadline = 5 * time.Second

// PolicyLogStaleAfter is how long a host may serve without having reached the
// log.
//
// Not zero, deliberately: a log round trip on every request would make the log's
// availability the host's own, and a momentary blip would become an outage. Not
// unbounded either, because then "I cannot reach the log" would never become "I
// must stop serving". One minute is the window in which a narrowing that has
// been appended but not seen here could still be honoured by this host's local
// state; past it, the host no longer knows whether its authority is current.
const PolicyLogStaleAfter = time.Minute

// WithPolicyLoggedNarrowing runs a narrowing under the protocol.
//
// The sequence, and why each step is where it is:
//
//  1. VALIDATE the entry. An entry the log could not be replayed from must not
//     reach the log, because a bad entry that is accepted is worse than a
//     refused operation: it is a gap nothing can close.
//  2. APPEND, under a bounded deadline, BEFORE any transaction opens. A slow log
//     then costs a refusal rather than held locks.
//  3. RECORD the receipt locally. From this instant the operation is a GAP, and
//     every replica will enforce it or refuse until it is committed.
//  4. APPLY the narrowing and COMMIT the receipt in ONE transaction. Either both
//     land or neither does, so "applied but unwitnessed" cannot exist.
//
// A failure at (4) leaves the gap from (3), which is the fail-closed state: the
// log says the narrowing happened, so the host enforces it or stops serving.
func (s *Service) WithPolicyLoggedNarrowing(
	ctx context.Context, entry *PolicyLogEntry, apply func(ctx context.Context) error,
) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	if s.policyLog == nil || s.policyLogStore == nil {
		// Fail closed. A host with no log cannot narrow authority, because the
		// narrowing would be unwitnessed and a restore would undo it silently.
		return fmt.Errorf("%w: this host has no policy log, so authority cannot be narrowed", ErrPolicyLogUnreachable)
	}

	// The deadline wraps the append alone. Putting it around the whole operation
	// would make a slow DATABASE look like an unreachable log, and those have
	// different answers.
	appendCtx, cancel := context.WithTimeout(ctx, PolicyLogDeadline)
	defer cancel()

	receipt, err := s.policyLog.Append(appendCtx, entry)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPolicyLogUnreachable, err)
	}
	if receipt == nil || receipt.Receipt == "" || receipt.Sequence == 0 {
		// A log that returns no receipt has not witnessed anything, whatever it
		// returned. Refusing here rather than storing an empty receipt keeps
		// "witnessed" and "attempted" distinguishable.
		return fmt.Errorf("%w: the log returned no usable receipt for operation %q",
			ErrPolicyLogUnreachable, entry.OperationID)
	}

	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		_, recordErr := s.policyLogStore.RecordPolicyLogAppend(ctx, receipt, entry)
		return recordErr
	}); err != nil {
		// The gate's cached answer is dropped either way: the append landed,
		// so a gap exists whether or not this host recorded it.
		s.policyServing.invalidate()
		// The append landed and this host could not even record that it did.
		// The gap exists in the LOG, which reconciliation reads — so this is
		// reported rather than swallowed, and the next reconciliation pass finds
		// it there.
		return fmt.Errorf("record policy log append for operation %q (the append LANDED; reconciliation will find it): %w",
			entry.OperationID, err)
	}

	// From here the operation is a GAP, and every replica reading this
	// host's commit relation sees it. Drop the gate's cached answer so this
	// host's own next request re-reads rather than serving the decision it
	// made before the gap existed — and drop it again after the commit, so a
	// gap that just closed does not keep the host shut for the TTL.
	s.policyServing.invalidate()
	defer s.policyServing.invalidate()

	// The narrowing and its receipt in one transaction.
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := apply(ctx); err != nil {
			return err
		}
		return s.policyLogStore.CommitPolicyLogOperation(ctx, entry.OperationID, s.policyNow())
	})
}

// PolicyLogServingState is why a host may not be serving.
type PolicyLogServingState struct {
	// Unreachable is set when the log has not been reached within
	// PolicyLogStaleAfter.
	Unreachable bool
	// Gaps are appended operations this host has not applied.
	Gaps []*PolicyLogGap
	// LastReachedAt is when the log was last read, nil if never.
	LastReachedAt *time.Time
}

// MayServe reports whether the host may serve, and why not when it may not.
//
// Two conditions, and both are refusals rather than degradations:
//
//   - the log has not been reached within the staleness window. The host does
//     not know whether its authority is current, and serving what it last
//     believed is serving authority that may have been revoked.
//   - there is an appended operation this host has not applied. The authority of
//     record says a narrowing happened; serving the wider authority still held
//     locally is serving authority that was revoked.
//
// Checked on the serving path and NOT only at startup, which is the requirement
// that makes it useful: a gap opens at runtime, when a commit fails after an
// append landed, and a startup-only check would serve through exactly the window
// the protocol exists for.
func (s *Service) MayServe(ctx context.Context) (bool, *PolicyLogServingState, error) {
	if s.policyLogStore == nil {
		// No log configured: nothing has been narrowed through the protocol, so
		// there is nothing unreconciled. Serving is allowed, and the host's
		// inability to narrow authority is reported by
		// WithPolicyLoggedNarrowing rather than by refusing every request.
		return true, &PolicyLogServingState{}, nil
	}
	state := &PolicyLogServingState{}
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		_, reachedAt, err := s.policyLogStore.PolicyLogCursorState(ctx)
		if err != nil {
			return err
		}
		state.LastReachedAt = reachedAt
		gaps, err := s.policyLogStore.UncommittedPolicyLogOperations(ctx)
		if err != nil {
			return err
		}
		state.Gaps = gaps
		return nil
	}); err != nil {
		return false, nil, err
	}
	if state.LastReachedAt == nil || s.policyNow().Sub(*state.LastReachedAt) > PolicyLogStaleAfter {
		state.Unreachable = true
	}
	return !state.Unreachable && len(state.Gaps) == 0, state, nil
}

// ReconcilePolicyLog reads the log forward and closes what it can.
//
// It runs on an interval, not only at startup. A gap opens at RUNTIME — a commit
// that failed after its append landed — and the requirement is that every
// replica enforces or refuses until reconciled, which a startup-only pass cannot
// provide.
//
// What it does NOT do is decide authority from the log. The log is the record;
// applying an entry means re-running the narrowing it describes, which is the
// caller's domain logic and not this function's. So reconciliation reports the
// gaps and advances the cursor, and the serving gate keeps the host closed until
// something has closed them. That division is deliberate: a reconciler that
// re-derived authority from log payloads would be a second implementation of
// every narrowing, and the copy is what drifts.
func (s *Service) ReconcilePolicyLog(ctx context.Context) (*PolicyLogServingState, error) {
	if s.policyLog == nil || s.policyLogStore == nil {
		return &PolicyLogServingState{}, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, PolicyLogDeadline)
	defer cancel()

	var cursor uint64
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		cursor, _, err = s.policyLogStore.PolicyLogCursorState(ctx)
		return err
	}); err != nil {
		return nil, err
	}

	records, err := s.policyLog.Entries(readCtx, cursor, policyLogReadBatch)
	if err != nil {
		// Unreachable. The cursor is NOT advanced, so the staleness window
		// closes and the host stops serving — which is the point: a host that
		// cannot read the log does not know whether its authority is current.
		return nil, fmt.Errorf("%w: %w", ErrPolicyLogUnreachable, err)
	}

	now := s.policyNow()
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		highest := cursor
		for _, record := range records {
			// An entry this host has never seen is recorded as a GAP before the
			// cursor passes it. Recording first is what makes the cursor safe to
			// advance: if the pass dies here, the entries already written are
			// gaps the serving gate sees, and the cursor still points below them.
			if _, err := s.policyLogStore.RecordPolicyLogAppend(ctx, &PolicyLogReceipt{
				Receipt:    record.Receipt,
				Sequence:   record.Sequence,
				AppendedAt: record.LoggedAt,
			}, &record.PolicyLogEntry); err != nil {
				return err
			}
			if record.Sequence > highest {
				highest = record.Sequence
			}
		}
		return s.policyLogStore.AdvancePolicyLogCursor(ctx, highest, now)
	}); err != nil {
		return nil, err
	}

	_, state, err := s.MayServe(ctx)
	return state, err
}

// policyLogReadBatch bounds one reconciliation read. Bounded because the reader
// holds no job-creation permission on the log's warehouse — this is a row read
// with a restriction, and an unbounded one would be a scan.
const policyLogReadBatch = 500

// policyNow is the host's clock, overridable in tests. Separate from the log's
// clock on purpose: receipts carry the LOG'S time, and comparing a host's clock
// against a log's ordering would compare two unrelated things.
func (s *Service) policyNow() time.Time {
	if s.policyClock != nil {
		return s.policyClock()
	}
	return time.Now().UTC()
}
