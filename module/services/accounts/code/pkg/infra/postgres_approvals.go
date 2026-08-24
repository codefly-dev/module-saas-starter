package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// =====================================================================
// saas/approvals/v1 — Postgres adapter
// =====================================================================
//
// Implements business.ApprovalStore. The interesting bits:
//
//   - **LockApprovalRequest** does SELECT ... FOR UPDATE so concurrent
//     deciders on the same request serialize on the head row; the engine's
//     Decide runs the whole read-decide-transition under one tx, making
//     the Nth-approve transition happen exactly once.
//   - **InsertDecision** maps the UNIQUE (request_id, decider) violation to
//     ErrTypeConflict — a double-vote is rejected by the constraint, not by
//     application logic.
//   - All queries are org-scoped and run through the RLS tx executor.

const approvalRequestColumns = `
    id, org_id, resource, action, subject, requested_by, quorum, policy, state,
    resume_ref, expires_at, escalate_at, COALESCE(decision_reason, ''),
    decided_at, created_at, updated_at
`

func scanApprovalRequest(row pgx.Row) (*business.ApprovalRequest, error) {
	var r business.ApprovalRequest
	var subjectJSON, policyJSON, resumeJSON []byte
	var state string
	err := row.Scan(
		&r.ID, &r.OrgID, &r.Resource, &r.Action, &subjectJSON, &r.RequestedBy,
		&r.Quorum, &policyJSON, &state, &resumeJSON, &r.ExpiresAt, &r.EscalateAt,
		&r.DecisionReason, &r.DecidedAt, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.State = business.ApprovalState(state)
	// Fail closed on a malformed policy: a policy that silently unmarshals to the
	// zero value would drop the approver-set restriction and the self-approval
	// block, so a corrupt row must surface as an error, never as a permissive
	// default. subject and resume_ref get the same treatment for integrity.
	if len(subjectJSON) > 0 {
		if err := json.Unmarshal(subjectJSON, &r.Subject); err != nil {
			return nil, fmt.Errorf("unmarshal approval subject: %w", err)
		}
	}
	if len(policyJSON) > 0 {
		if err := json.Unmarshal(policyJSON, &r.Policy); err != nil {
			return nil, fmt.Errorf("unmarshal approval policy: %w", err)
		}
	}
	if len(resumeJSON) > 0 {
		if err := json.Unmarshal(resumeJSON, &r.ResumeRef); err != nil {
			return nil, fmt.Errorf("unmarshal approval resume_ref: %w", err)
		}
	}
	return &r, nil
}

// InsertApprovalRequest implements business.ApprovalStore.
func (s *PostgresStore) InsertApprovalRequest(ctx context.Context, r *business.ApprovalRequest) error {
	w := wool.Get(ctx).In("InsertApprovalRequest",
		wool.Field("resource", r.Resource), wool.Field("action", r.Action))
	executor := s.getQueryExecutor(ctx)

	subjectJSON := jsonOrObject(r.Subject)
	policyJSON, err := json.Marshal(r.Policy)
	if err != nil {
		return w.Wrapf(err, "marshal policy")
	}
	resumeJSON, err := json.Marshal(r.ResumeRef)
	if err != nil {
		return w.Wrapf(err, "marshal resume_ref")
	}

	_, err = executor.Exec(ctx, approvalInsert,
		r.ID, r.OrgID, r.Resource, r.Action, subjectJSON, r.RequestedBy,
		r.Quorum, policyJSON, string(r.State), resumeJSON, r.ExpiresAt, r.EscalateAt,
	)
	if err != nil {
		return w.Wrapf(err, "insert approval request")
	}
	return nil
}

// GetApprovalRequest implements business.ApprovalStore.
func (s *PostgresStore) GetApprovalRequest(ctx context.Context, id, orgID string) (*business.ApprovalRequest, error) {
	return s.getApprovalRequest(ctx, id, orgID, false)
}

// LockApprovalRequest implements business.ApprovalStore (SELECT ... FOR UPDATE).
func (s *PostgresStore) LockApprovalRequest(ctx context.Context, id, orgID string) (*business.ApprovalRequest, error) {
	return s.getApprovalRequest(ctx, id, orgID, true)
}

func (s *PostgresStore) getApprovalRequest(ctx context.Context, id, orgID string, forUpdate bool) (*business.ApprovalRequest, error) {
	w := wool.Get(ctx).In("GetApprovalRequest", wool.Field("approval_id", id))
	executor := s.getQueryExecutor(ctx)

	query := approvalSelectByID
	if forUpdate {
		query = approvalSelectByIDForUpdate
	}
	r, err := scanApprovalRequest(executor.QueryRow(ctx, query, id, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(
				fmt.Errorf("approval request %s not found", id),
				business.ErrTypeNotFound,
			)
		}
		return nil, w.Wrapf(err, "get approval request")
	}
	return r, nil
}

// ListApprovalRequests implements business.ApprovalStore.
func (s *PostgresStore) ListApprovalRequests(ctx context.Context, orgID, state string, pageSize int32, pageToken string) ([]*business.ApprovalRequest, string, error) {
	w := wool.Get(ctx).In("ListApprovalRequests", wool.Field("org_id", orgID))
	executor := s.getQueryExecutor(ctx)

	args := []any{orgID}
	query := `SELECT ` + approvalRequestColumns + ` FROM approval_requests WHERE org_id = $1`
	n := 1
	if state != "" {
		n++
		query += fmt.Sprintf(" AND state = $%d", n)
		args = append(args, state)
	}
	if pageToken != "" {
		// Simple cursor: id-keyed, matching the delegation list pattern. Stable
		// enough for the approval queue; a composite (created_at,id) keyset can
		// replace it if strict cross-second ordering ever matters.
		n++
		query += fmt.Sprintf(" AND id < $%d", n)
		args = append(args, pageToken)
	}
	n++
	query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", n)
	args = append(args, pageSize+1)

	rows, err := executor.Query(ctx, query, args...)
	if err != nil {
		return nil, "", w.Wrapf(err, "query approval requests")
	}
	defer rows.Close()

	out := []*business.ApprovalRequest{}
	for rows.Next() {
		r, err := scanApprovalRequest(rows)
		if err != nil {
			return nil, "", w.Wrapf(err, "scan approval request")
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", w.Wrapf(err, "iterate approval requests")
	}

	next := ""
	if int32(len(out)) > pageSize {
		next = out[pageSize-1].ID
		out = out[:pageSize]
	}
	return out, next, nil
}

// InsertDecision implements business.ApprovalStore. A double-vote trips the
// UNIQUE (request_id, decider) constraint and surfaces as ErrTypeConflict.
func (s *PostgresStore) InsertDecision(ctx context.Context, d *business.ApprovalDecision) error {
	w := wool.Get(ctx).In("InsertApprovalDecision",
		wool.Field("approval_id", d.RequestID), wool.Field("decider", d.Decider))
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, approvalInsertDecision,
		d.ID, d.RequestID, d.OrgID, d.Decider, string(d.Decision), d.Reason, d.DelegationGrantID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return business.NewStoreError(
				fmt.Errorf("decider %s already decided approval %s", d.Decider, d.RequestID),
				business.ErrTypeConflict,
			)
		}
		return w.Wrapf(err, "insert approval decision")
	}
	return nil
}

// CountApprovals implements business.ApprovalStore.
func (s *PostgresStore) CountApprovals(ctx context.Context, requestID, orgID string) (int, error) {
	w := wool.Get(ctx).In("CountApprovals", wool.Field("approval_id", requestID))
	executor := s.getQueryExecutor(ctx)

	var count int
	if err := executor.QueryRow(ctx, approvalCountApproves, requestID, orgID).Scan(&count); err != nil {
		return 0, w.Wrapf(err, "count approvals")
	}
	return count, nil
}

// UpdateApprovalState implements business.ApprovalStore.
func (s *PostgresStore) UpdateApprovalState(ctx context.Context, id, orgID string, to business.ApprovalState, reason string, setDecided bool) error {
	w := wool.Get(ctx).In("UpdateApprovalState",
		wool.Field("approval_id", id), wool.Field("to", string(to)))
	executor := s.getQueryExecutor(ctx)

	tag, err := executor.Exec(ctx, approvalUpdateState, string(to), reason, setDecided, id, orgID)
	if err != nil {
		return w.Wrapf(err, "update approval state")
	}
	if tag.RowsAffected() == 0 {
		return business.NewStoreError(
			fmt.Errorf("approval request %s not found in org %s", id, orgID),
			business.ErrTypeNotFound,
		)
	}
	return nil
}

// jsonOrObject marshals v, returning "{}" for a nil/empty map so the JSONB
// column never receives SQL NULL or the literal "null".
func jsonOrObject(v map[string]any) []byte {
	if len(v) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// =====================================================================
// Compile-time assertion
// =====================================================================

var _ business.ApprovalStore = (*PostgresStore)(nil)

// =====================================================================
// SQL
// =====================================================================

var (
	approvalSelectByID = `
        SELECT ` + approvalRequestColumns + `
        FROM approval_requests
        WHERE id = $1 AND org_id = $2
    `

	approvalSelectByIDForUpdate = approvalSelectByID + ` FOR UPDATE`

	approvalInsert = `
        INSERT INTO approval_requests (
            id, org_id, resource, action, subject, requested_by,
            quorum, policy, state, resume_ref, expires_at, escalate_at
        )
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
    `

	approvalInsertDecision = `
        INSERT INTO approval_decisions (
            id, request_id, org_id, decider, decision, reason, delegation_grant_id
        )
        VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, '')::uuid)
    `

	approvalCountApproves = `
        SELECT count(*)
        FROM approval_decisions
        WHERE request_id = $1 AND org_id = $2 AND decision = 'approve'
    `

	approvalUpdateState = `
        UPDATE approval_requests
        SET state = $1,
            decision_reason = NULLIF($2, ''),
            decided_at = CASE WHEN $3 THEN CURRENT_TIMESTAMP ELSE decided_at END,
            updated_at = CURRENT_TIMESTAMP
        WHERE id = $4 AND org_id = $5
    `
)
