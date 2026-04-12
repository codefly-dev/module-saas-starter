package infra

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"api/pkg/business"
)

// Compile-time check that PostgresStore implements MFAStore.
var _ business.MFAStore = (*PostgresStore)(nil)

func (s *PostgresStore) CreateMFADevice(ctx context.Context, device *business.MFADevice) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx, `
		INSERT INTO mfa_devices (id, user_id, device_type, name, secret_encrypted, verified_at, last_used_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		device.ID, device.UserID, device.DeviceType, device.Name,
		device.SecretEncrypted, device.VerifiedAt, device.LastUsedAt)
	return err
}

func (s *PostgresStore) GetMFADevice(ctx context.Context, id string) (*business.MFADevice, error) {
	q := s.getQueryExecutor(ctx)

	row := q.QueryRow(ctx, `
		SELECT id, user_id, device_type, name, secret_encrypted, verified_at, last_used_at, created_at
		FROM mfa_devices WHERE id = $1`, id)

	var device business.MFADevice
	var verifiedAt, lastUsedAt *time.Time

	err := row.Scan(&device.ID, &device.UserID, &device.DeviceType, &device.Name,
		&device.SecretEncrypted, &verifiedAt, &lastUsedAt, &device.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(errors.New("MFA device not found"), business.ErrTypeNotFound)
		}
		return nil, err
	}

	device.VerifiedAt = verifiedAt
	device.LastUsedAt = lastUsedAt
	return &device, nil
}

func (s *PostgresStore) ListMFADevices(ctx context.Context, userID string) ([]*business.MFADevice, error) {
	q := s.getQueryExecutor(ctx)

	rows, err := q.Query(ctx, `
		SELECT id, user_id, device_type, name, secret_encrypted, verified_at, last_used_at, created_at
		FROM mfa_devices WHERE user_id = $1
		ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []*business.MFADevice
	for rows.Next() {
		var device business.MFADevice
		var verifiedAt, lastUsedAt *time.Time

		if err := rows.Scan(&device.ID, &device.UserID, &device.DeviceType, &device.Name,
			&device.SecretEncrypted, &verifiedAt, &lastUsedAt, &device.CreatedAt); err != nil {
			return nil, err
		}

		device.VerifiedAt = verifiedAt
		device.LastUsedAt = lastUsedAt
		devices = append(devices, &device)
	}

	return devices, nil
}

func (s *PostgresStore) DeleteMFADevice(ctx context.Context, id string) error {
	q := s.getQueryExecutor(ctx)

	tag, err := q.Exec(ctx, `DELETE FROM mfa_devices WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return business.NewStoreError(errors.New("MFA device not found"), business.ErrTypeNotFound)
	}
	return nil
}

func (s *PostgresStore) UpdateMFADevice(ctx context.Context, device *business.MFADevice) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx, `
		UPDATE mfa_devices
		SET name = $2, secret_encrypted = $3, verified_at = $4, last_used_at = $5
		WHERE id = $1`,
		device.ID, device.Name, device.SecretEncrypted, device.VerifiedAt, device.LastUsedAt)
	return err
}

func (s *PostgresStore) HasVerifiedMFA(ctx context.Context, userID string) (bool, error) {
	q := s.getQueryExecutor(ctx)

	var count int
	err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM mfa_devices
		WHERE user_id = $1 AND verified_at IS NOT NULL`, userID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *PostgresStore) CreateBackupCodes(ctx context.Context, codes []*business.MFABackupCode) error {
	q := s.getQueryExecutor(ctx)

	for _, code := range codes {
		_, err := q.Exec(ctx, `
			INSERT INTO mfa_backup_codes (id, user_id, code_hash)
			VALUES ($1, $2, $3)`,
			code.ID, code.UserID, code.CodeHash)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) GetUnusedBackupCodes(ctx context.Context, userID string) ([]*business.MFABackupCode, error) {
	q := s.getQueryExecutor(ctx)

	rows, err := q.Query(ctx, `
		SELECT id, user_id, code_hash, used_at
		FROM mfa_backup_codes
		WHERE user_id = $1 AND used_at IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var codes []*business.MFABackupCode
	for rows.Next() {
		var code business.MFABackupCode
		var usedAt *time.Time
		if err := rows.Scan(&code.ID, &code.UserID, &code.CodeHash, &usedAt); err != nil {
			return nil, err
		}
		code.UsedAt = usedAt
		codes = append(codes, &code)
	}

	return codes, nil
}

func (s *PostgresStore) UseBackupCode(ctx context.Context, id string) error {
	q := s.getQueryExecutor(ctx)

	tag, err := q.Exec(ctx, `
		UPDATE mfa_backup_codes SET used_at = NOW()
		WHERE id = $1 AND used_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return business.NewStoreError(errors.New("backup code not found or already used"), business.ErrTypeNotFound)
	}
	return nil
}

func (s *PostgresStore) DeleteBackupCodes(ctx context.Context, userID string) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx, `DELETE FROM mfa_backup_codes WHERE user_id = $1`, userID)
	return err
}
