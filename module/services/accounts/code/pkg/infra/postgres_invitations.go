package infra

import (
	"context"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

const invitationColumns = `
	id, org_id, inviter_id, inviter_display_name, email, role, token_hash,
	status, delivery_status, expires_at, accepted_at, accepted_by, created_at,
	last_sent_at, send_count`

func (s *PostgresStore) CreateInvitation(ctx context.Context, inv *business.Invitation) error {
	if inv.DeliveryStatus == "" {
		inv.DeliveryStatus = "disabled"
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO invitations (
			id, org_id, inviter_id, inviter_display_name, email, role, token_hash,
			status, delivery_status, expires_at, last_sent_at, send_count
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		inv.ID, inv.OrgID, inv.InviterID, inv.InviterDisplayName, inv.Email,
		inv.Role, inv.TokenHash, inv.Status, inv.DeliveryStatus, inv.ExpiresAt,
		inv.LastSentAt, inv.SendCount)
	return err
}

func (s *PostgresStore) ExpirePendingInvitations(ctx context.Context, orgID string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE invitations
		SET status = 'expired'
		WHERE org_id = $1 AND status = 'pending' AND expires_at <= CURRENT_TIMESTAMP`,
		orgID)
	return err
}

func (s *PostgresStore) GetInvitationByTokenHash(
	ctx context.Context,
	hash string,
) (*business.Invitation, error) {
	return s.getInvitation(ctx, "token_hash", hash)
}

func (s *PostgresStore) GetInvitationByID(
	ctx context.Context,
	id string,
) (*business.Invitation, error) {
	return s.getInvitation(ctx, "id", id)
}

func (s *PostgresStore) getInvitation(
	ctx context.Context,
	column, value string,
) (*business.Invitation, error) {
	query := `SELECT ` + invitationColumns + ` FROM invitations WHERE ` + column + ` = $1 FOR UPDATE`
	row := s.getQueryExecutor(ctx).QueryRow(ctx, query, value)
	var inv business.Invitation
	var acceptedBy *string
	err := row.Scan(
		&inv.ID,
		&inv.OrgID,
		&inv.InviterID,
		&inv.InviterDisplayName,
		&inv.Email,
		&inv.Role,
		&inv.TokenHash,
		&inv.Status,
		&inv.DeliveryStatus,
		&inv.ExpiresAt,
		&inv.AcceptedAt,
		&acceptedBy,
		&inv.CreatedAt,
		&inv.LastSentAt,
		&inv.SendCount,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if acceptedBy != nil {
		inv.AcceptedBy = *acceptedBy
	}
	return &inv, nil
}

func (s *PostgresStore) GetInvitationOrgID(ctx context.Context, id string) (string, error) {
	var orgID string
	err := s.getQueryExecutor(ctx).QueryRow(
		ctx, `SELECT org_id FROM invitations WHERE id = $1`, id,
	).Scan(&orgID)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return orgID, err
}

func (s *PostgresStore) ListInvitations(
	ctx context.Context,
	orgID, status string,
) ([]*business.Invitation, error) {
	query := `SELECT ` + invitationColumns + ` FROM invitations WHERE org_id = $1`
	args := []any{orgID}
	if status != "" {
		query += ` AND status = $2`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.getQueryExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invitations []*business.Invitation
	for rows.Next() {
		var inv business.Invitation
		var acceptedBy *string
		if err := rows.Scan(
			&inv.ID,
			&inv.OrgID,
			&inv.InviterID,
			&inv.InviterDisplayName,
			&inv.Email,
			&inv.Role,
			&inv.TokenHash,
			&inv.Status,
			&inv.DeliveryStatus,
			&inv.ExpiresAt,
			&inv.AcceptedAt,
			&acceptedBy,
			&inv.CreatedAt,
			&inv.LastSentAt,
			&inv.SendCount,
		); err != nil {
			return nil, err
		}
		if acceptedBy != nil {
			inv.AcceptedBy = *acceptedBy
		}
		invitations = append(invitations, &inv)
	}
	return invitations, rows.Err()
}

func (s *PostgresStore) RotateInvitationToken(
	ctx context.Context,
	inv *business.Invitation,
) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE invitations
		SET token_hash = $2, expires_at = $3, last_sent_at = $4,
		    send_count = $5, delivery_status = $6
		WHERE id = $1 AND status = 'pending'`,
		inv.ID,
		inv.TokenHash,
		inv.ExpiresAt,
		inv.LastSentAt,
		inv.SendCount,
		inv.DeliveryStatus,
	)
	return err
}

func (s *PostgresStore) UpdateInvitationStatus(
	ctx context.Context,
	id, status, acceptedBy string,
) error {
	switch status {
	case "accepted":
		_, err := s.getQueryExecutor(ctx).Exec(ctx, `
			UPDATE invitations
			SET status = 'accepted', accepted_at = COALESCE(accepted_at, CURRENT_TIMESTAMP),
			    accepted_by = COALESCE(accepted_by, $2)
			WHERE id = $1 AND status IN ('pending', 'accepted')`,
			id, nilIfEmpty(acceptedBy))
		return err
	case "revoked":
		_, err := s.getQueryExecutor(ctx).Exec(ctx, `
			UPDATE invitations
			SET status = 'revoked', revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
			WHERE id = $1 AND status = 'pending'`, id)
		return err
	default:
		_, err := s.getQueryExecutor(ctx).Exec(ctx, `
			UPDATE invitations SET status = $2
			WHERE id = $1 AND status = 'pending'`, id, status)
		return err
	}
}

func (s *PostgresStore) CountPendingInvitations(ctx context.Context, orgID string) (int32, error) {
	var count int32
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT COUNT(*)
		FROM invitations
		WHERE org_id = $1 AND status = 'pending' AND expires_at > CURRENT_TIMESTAMP`,
		orgID).Scan(&count)
	return count, err
}

func (s *PostgresStore) HasPendingInvitationForEmail(
	ctx context.Context,
	email string,
) (bool, error) {
	var exists bool
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM invitations
			WHERE LOWER(email) = LOWER($1)
			  AND status = 'pending'
			  AND expires_at > CURRENT_TIMESTAMP
		)`, email).Scan(&exists)
	return exists, err
}
