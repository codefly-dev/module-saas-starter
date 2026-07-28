package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"accounts/pkg/business"
)

func (s *PostgresStore) CreateInvitation(ctx context.Context, inv *business.Invitation) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO invitations (
			id, org_id, inviter_id, email, role, token_hash, status, expires_at, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		inv.ID, inv.OrgID, inv.InviterID, inv.Email, inv.Role, inv.TokenHash,
		inv.Status, inv.ExpiresAt, inv.CreatedAt)
	return err
}

// ExpirePendingInvitations releases stale seat reservations and, importantly,
// removes them from the partial unique index that permits only one pending
// invitation per organization/email.
func (s *PostgresStore) ExpirePendingInvitations(ctx context.Context, orgID string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE invitations
		SET status = 'expired'
		WHERE org_id = $1
		  AND status = 'pending'
		  AND expires_at <= CURRENT_TIMESTAMP`, orgID)
	return err
}

func (s *PostgresStore) GetInvitationByTokenHash(ctx context.Context, hash string) (*business.Invitation, error) {
	q := s.getQueryExecutor(ctx)
	row := q.QueryRow(ctx, `
		SELECT id, org_id, inviter_id, email, role, token_hash, status, expires_at, accepted_at, accepted_by, created_at
		FROM invitations WHERE token_hash = $1`, hash)

	var inv business.Invitation
	var acceptedAt *time.Time
	var acceptedBy *string

	err := row.Scan(&inv.ID, &inv.OrgID, &inv.InviterID, &inv.Email, &inv.Role,
		&inv.TokenHash, &inv.Status, &inv.ExpiresAt, &acceptedAt, &acceptedBy, &inv.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if acceptedAt != nil {
		inv.AcceptedAt = acceptedAt
	}
	if acceptedBy != nil {
		inv.AcceptedBy = *acceptedBy
	}
	return &inv, nil
}

func (s *PostgresStore) GetInvitationByID(
	ctx context.Context,
	id string,
) (*business.Invitation, error) {
	q := s.getQueryExecutor(ctx)
	row := q.QueryRow(ctx, `
		SELECT id, org_id, inviter_id, email, role, token_hash, status, expires_at,
		       accepted_at, accepted_by, created_at
		FROM invitations
		WHERE id = $1`, id)

	var inv business.Invitation
	var acceptedAt *time.Time
	var acceptedBy *string
	err := row.Scan(
		&inv.ID,
		&inv.OrgID,
		&inv.InviterID,
		&inv.Email,
		&inv.Role,
		&inv.TokenHash,
		&inv.Status,
		&inv.ExpiresAt,
		&acceptedAt,
		&acceptedBy,
		&inv.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	inv.AcceptedAt = acceptedAt
	if acceptedBy != nil {
		inv.AcceptedBy = *acceptedBy
	}
	return &inv, nil
}

func (s *PostgresStore) GetInvitationOrgID(ctx context.Context, id string) (string, error) {
	q := s.getQueryExecutor(ctx)
	var orgID string
	err := q.QueryRow(ctx, `SELECT org_id FROM invitations WHERE id = $1`, id).Scan(&orgID)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return orgID, err
}

func (s *PostgresStore) ListInvitations(ctx context.Context, orgID string, status string) ([]*business.Invitation, error) {
	q := s.getQueryExecutor(ctx)

	query := `SELECT id, org_id, inviter_id, email, role, status, expires_at, created_at
		FROM invitations WHERE org_id = $1`
	args := []any{orgID}

	if status != "" {
		query += ` AND status = $2`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invitations []*business.Invitation
	for rows.Next() {
		var inv business.Invitation
		err := rows.Scan(&inv.ID, &inv.OrgID, &inv.InviterID, &inv.Email, &inv.Role,
			&inv.Status, &inv.ExpiresAt, &inv.CreatedAt)
		if err != nil {
			return nil, err
		}
		invitations = append(invitations, &inv)
	}
	return invitations, nil
}

func (s *PostgresStore) UpdateInvitationStatus(
	ctx context.Context,
	id string,
	status string,
	acceptedBy string,
	occurredAt time.Time,
) error {
	q := s.getQueryExecutor(ctx)
	var result pgconn.CommandTag
	var err error
	if status == "accepted" && acceptedBy != "" {
		result, err = q.Exec(ctx, `
			UPDATE invitations
			SET status = $2, accepted_at = $4, accepted_by = $3
			WHERE id = $1 AND status = 'pending'`,
			id, status, acceptedBy, occurredAt)
	} else if status == "revoked" {
		result, err = q.Exec(ctx, `
			UPDATE invitations
			SET status = $2, revoked_at = $3
			WHERE id = $1 AND status = 'pending'`,
			id, status, occurredAt)
	} else {
		result, err = q.Exec(ctx, `
			UPDATE invitations
			SET status = $2
			WHERE id = $1 AND status = 'pending'`,
			id, status)
	}
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return business.ErrInvitationTransitionConflict
	}
	return nil
}

func (s *PostgresStore) CountPendingInvitations(ctx context.Context, orgID string) (int32, error) {
	q := s.getQueryExecutor(ctx)
	var count int32
	err := q.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM invitations
		WHERE org_id = $1
		  AND status = 'pending'
		  AND expires_at > CURRENT_TIMESTAMP`, orgID).Scan(&count)
	return count, err
}
