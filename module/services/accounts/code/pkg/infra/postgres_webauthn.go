package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ business.WebAuthnStore = (*PostgresStore)(nil)

func (s *PostgresStore) CreateWebAuthnCeremony(ctx context.Context, ceremony *business.WebAuthnCeremony) error {
	q := s.getQueryExecutor(ctx)
	var loginTransactionID any
	if ceremony.MFALoginTransactionID != "" {
		loginTransactionID = ceremony.MFALoginTransactionID
	}
	_, err := q.Exec(ctx, `
		INSERT INTO webauthn_ceremonies (
			id, token_hash, user_id, mfa_login_transaction_id, ceremony_type,
			session_data_encrypted, expires_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		ceremony.ID, ceremony.TokenHash, ceremony.UserID, loginTransactionID,
		ceremony.CeremonyType, ceremony.SessionDataEncrypted, ceremony.ExpiresAt, ceremony.CreatedAt,
	)
	return err
}

func (s *PostgresStore) GetWebAuthnCeremonyForUpdate(
	ctx context.Context,
	tokenHash, userID, ceremonyType, mfaLoginTransactionID string,
	now time.Time,
) (*business.WebAuthnCeremony, error) {
	q := s.getQueryExecutor(ctx)
	var loginTransactionID any
	if mfaLoginTransactionID != "" {
		loginTransactionID = mfaLoginTransactionID
	}
	var ceremony business.WebAuthnCeremony
	var storedLoginTransactionID *string
	err := q.QueryRow(ctx, `
		SELECT id, token_hash, user_id, mfa_login_transaction_id::text,
		       ceremony_type, session_data_encrypted, expires_at,
		       consumed_at, created_at
		FROM webauthn_ceremonies
		WHERE token_hash = $1
		  AND user_id = $2
		  AND ceremony_type = $3
		  AND mfa_login_transaction_id IS NOT DISTINCT FROM $4::uuid
		FOR UPDATE`, tokenHash, userID, ceremonyType, loginTransactionID).Scan(
		&ceremony.ID, &ceremony.TokenHash, &ceremony.UserID, &storedLoginTransactionID,
		&ceremony.CeremonyType, &ceremony.SessionDataEncrypted, &ceremony.ExpiresAt,
		&ceremony.ConsumedAt, &ceremony.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(errors.New("WebAuthn ceremony not found"), business.ErrTypeNotFound)
		}
		return nil, err
	}
	if ceremony.ConsumedAt != nil || !now.Before(ceremony.ExpiresAt) {
		return nil, business.NewStoreError(errors.New("WebAuthn ceremony inactive"), business.ErrTypeNotFound)
	}
	if storedLoginTransactionID != nil {
		ceremony.MFALoginTransactionID = *storedLoginTransactionID
	}
	return &ceremony, nil
}

func (s *PostgresStore) ConsumeWebAuthnCeremony(ctx context.Context, id string, now time.Time) error {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE webauthn_ceremonies
		SET consumed_at = $2
		WHERE id = $1 AND consumed_at IS NULL AND expires_at > $2`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return business.NewStoreError(errors.New("WebAuthn ceremony already consumed"), business.ErrTypeNotFound)
	}
	return nil
}

func (s *PostgresStore) ListWebAuthnCredentials(ctx context.Context, userID string, forUpdate bool) ([]*business.StoredWebAuthnCredential, error) {
	query := `
		SELECT device_id, user_id, credential_id, credential_encrypted, created_at, updated_at
		FROM webauthn_credentials
		WHERE user_id = $1
		ORDER BY created_at`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var credentials []*business.StoredWebAuthnCredential
	for rows.Next() {
		credential := &business.StoredWebAuthnCredential{}
		if err := rows.Scan(
			&credential.DeviceID, &credential.UserID, &credential.CredentialID,
			&credential.CredentialEncrypted, &credential.CreatedAt, &credential.UpdatedAt,
		); err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	return credentials, rows.Err()
}

func (s *PostgresStore) CreateWebAuthnCredential(ctx context.Context, device *business.MFADevice, credential *business.StoredWebAuthnCredential) error {
	q := s.getQueryExecutor(ctx)
	if _, err := q.Exec(ctx, `
		INSERT INTO mfa_devices (
			id, user_id, device_type, name, secret_encrypted, verified_at, last_used_at
		) VALUES ($1, $2, 'webauthn', $3, NULL, $4, $5)`,
		device.ID, device.UserID, device.Name, device.VerifiedAt, device.LastUsedAt,
	); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `
		INSERT INTO webauthn_credentials (
			device_id, user_id, credential_id, credential_encrypted, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6)`,
		credential.DeviceID, credential.UserID, credential.CredentialID,
		credential.CredentialEncrypted, credential.CreatedAt, credential.UpdatedAt,
	)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return business.ErrWebAuthnCeremonyRejected
	}
	return err
}

func (s *PostgresStore) UpdateWebAuthnCredential(ctx context.Context, credential *business.StoredWebAuthnCredential, lastUsedAt time.Time) error {
	q := s.getQueryExecutor(ctx)
	tag, err := q.Exec(ctx, `
		UPDATE webauthn_credentials
		SET credential_encrypted = $3, updated_at = $4
		WHERE device_id = $1 AND credential_id = $2`,
		credential.DeviceID, credential.CredentialID, credential.CredentialEncrypted, lastUsedAt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return business.NewStoreError(errors.New("WebAuthn credential not found"), business.ErrTypeNotFound)
	}
	tag, err = q.Exec(ctx, `UPDATE mfa_devices SET last_used_at = $2 WHERE id = $1`, credential.DeviceID, lastUsedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return business.NewStoreError(errors.New("WebAuthn device not found"), business.ErrTypeNotFound)
	}
	return nil
}
