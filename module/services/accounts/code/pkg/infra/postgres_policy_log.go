package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

// The policy log's LOCAL half: receipts and the reconciliation cursor.
//
// The log itself is external, for the reason the protocol exists — a history
// stored beside the state it witnesses is backed up with it, so it cannot
// witness against it. What lives here is the receipt, and the only reachable
// inconsistency is "appended, not applied", which fails closed.
//
// Control-plane only. Neither relation is deletable: a receipt is the evidence
// that an append happened, so deleting one would make an unreconciled gap
// disappear rather than be closed.

// RecordPolicyLogAppend writes the receipt for an appended operation, with no
// commit yet.
//
// Idempotent on the operation id, and the `DO NOTHING` here is correct where the
// delivery inbox's was not: an operation id is a single logical operation, so a
// second append with the same id IS the same operation and the stored receipt
// is the authoritative one. There are no "different bytes at the same key" to
// distinguish — the log guarantees one entry per operation id, which is why that
// guarantee is the log's and not this host's.
func (s *PostgresStore) RecordPolicyLogAppend(
	ctx context.Context, receipt *business.PolicyLogReceipt, entry *business.PolicyLogEntry,
) (bool, error) {
	var inserted bool
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		INSERT INTO public.policy_log_commits
			(operation_id, receipt, log_sequence, subject_kind, subject_id, appended_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (operation_id) DO NOTHING
		RETURNING TRUE`,
		entry.OperationID, receipt.Receipt, int64(receipt.Sequence),
		string(entry.SubjectKind), entry.SubjectID, receipt.AppendedAt,
	).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return inserted, nil
}

// CommitPolicyLogOperation marks an appended operation applied.
//
// It runs in the SAME transaction as the narrowing, which is what makes
// "applied but unwitnessed" impossible. The `committed_at IS NULL` predicate
// makes a double commit a no-op rather than moving the timestamp: the first
// commit is when the narrowing took effect, and a retry must not rewrite that.
//
// A commit for an operation that was never appended is an ERROR, not a silent
// no-op. It would mean a narrowing reached this point without the log having
// witnessed it, which is the one direction the protocol forbids outright.
func (s *PostgresStore) CommitPolicyLogOperation(
	ctx context.Context, operationID string, at time.Time,
) error {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE public.policy_log_commits
		   SET committed_at = $2
		 WHERE operation_id = $1 AND committed_at IS NULL`, operationID, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// Either already committed (fine, a retry) or never appended (not fine).
	var appended bool
	if err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT TRUE FROM public.policy_log_commits WHERE operation_id = $1`, operationID,
	).Scan(&appended); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return business.NewStoreError(
				errors.New("cannot commit policy log operation "+operationID+
					": no append was recorded for it, so the narrowing would be unwitnessed"),
				business.ErrTypeConflict)
		}
		return err
	}
	return nil
}

// UncommittedPolicyLogOperations returns appended operations with no commit —
// the gaps the serving gate refuses on.
//
// Ordered by sequence so an operator reading them sees the log's order rather
// than this host's insertion order, which can differ: reconciliation records a
// batch of entries it has just read, and their sequences are the log's.
func (s *PostgresStore) UncommittedPolicyLogOperations(
	ctx context.Context,
) ([]*business.PolicyLogGap, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT operation_id, log_sequence, subject_kind, subject_id, appended_at
		  FROM public.policy_log_commits
		 WHERE committed_at IS NULL
		 ORDER BY log_sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	gaps := []*business.PolicyLogGap{}
	for rows.Next() {
		var (
			gap      business.PolicyLogGap
			sequence int64
			kind     string
		)
		if err := rows.Scan(&gap.OperationID, &sequence, &kind, &gap.SubjectID, &gap.AppendedAt); err != nil {
			return nil, err
		}
		gap.Sequence = uint64(sequence)
		gap.SubjectKind = business.PolicyLogSubjectKind(kind)
		gaps = append(gaps, &gap)
	}
	return gaps, rows.Err()
}

// PolicyLogCursorState reads how far this host has reconciled, and when it last
// reached the log.
func (s *PostgresStore) PolicyLogCursorState(
	ctx context.Context,
) (uint64, *time.Time, error) {
	var (
		sequence  int64
		reachedAt *time.Time
	)
	if err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT reconciled_sequence, reached_at FROM public.policy_log_cursor WHERE id = TRUE`,
	).Scan(&sequence, &reachedAt); err != nil {
		return 0, nil, err
	}
	return uint64(sequence), reachedAt, nil
}

// AdvancePolicyLogCursor records that the log was reached and read to a
// sequence.
//
// The sequence only ever MOVES FORWARD — `GREATEST` rather than assignment. Two
// replicas reconcile independently, so a slower one finishing second must not
// rewind the cursor and cause entries to be re-read and re-recorded as gaps that
// were already closed.
//
// `reached_at` is set unconditionally, because reaching the log is a fact about
// now regardless of whether there was anything new to read. A host that only
// updated it when entries arrived would appear stale during a quiet period and
// would stop serving for lack of news.
func (s *PostgresStore) AdvancePolicyLogCursor(
	ctx context.Context, sequence uint64, at time.Time,
) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE public.policy_log_cursor
		   SET reconciled_sequence = GREATEST(reconciled_sequence, $1),
		       reached_at = $2,
		       updated_at = $2
		 WHERE id = TRUE`, int64(sequence), at)
	return err
}

var _ business.PolicyLogStore = (*PostgresStore)(nil)
