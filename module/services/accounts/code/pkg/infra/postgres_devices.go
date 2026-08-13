package infra

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"accounts/pkg/business"
)

// Linked devices (linked_devices, device_claim_codes). Both tables are
// RLS-protected (direct org_id, migration 86): tenant paths run inside
// WithOrgTx; the two cross-tenant lookups (code hash, device public key)
// run under the audited control-plane scope via As(System()).

const deviceColumns = `
	id, org_id, device_public_key, name, created_by, created_at, revoked_at`

const deviceClaimCodeColumns = `
	id, org_id, code_hash, created_by, status, expires_at, created_at,
	used_at, used_by_device_id`

func (s *PostgresStore) CreateDevice(ctx context.Context, device *business.Device) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO linked_devices (id, org_id, device_public_key, name, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		device.ID, device.OrgID, device.DevicePublicKey, device.Name,
		nilIfEmpty(device.CreatedBy), device.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// device_public_key is globally unique: the device is already linked
		// (possibly to another org — RLS hides the row, the constraint holds).
		return business.NewStoreError(err, business.ErrTypeConflict)
	}
	return err
}

func (s *PostgresStore) GetDeviceByPublicKey(ctx context.Context, publicKey string) (*business.Device, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT `+deviceColumns+` FROM linked_devices WHERE device_public_key = $1`, publicKey)
	return scanDevice(row)
}

func (s *PostgresStore) ListDevices(ctx context.Context, orgID string) ([]*business.Device, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT `+deviceColumns+` FROM linked_devices WHERE org_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []*business.Device
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

// CountActiveDevices is the authoritative paired_devices cardinality. Must
// run inside the same tenant transaction as the admission lock and the
// insert.
func (s *PostgresStore) CountActiveDevices(ctx context.Context, orgID string) (int64, error) {
	var count int64
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT COUNT(*) FROM linked_devices
		WHERE org_id = $1 AND revoked_at IS NULL`, orgID).Scan(&count)
	return count, err
}

func (s *PostgresStore) RevokeDevice(ctx context.Context, deviceID, orgID string) (bool, error) {
	result, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE linked_devices
		SET revoked_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		deviceID, orgID)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *PostgresStore) CreateDeviceClaimCode(ctx context.Context, code *business.DeviceClaimCode) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO device_claim_codes (id, org_id, code_hash, created_by, status, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		code.ID, code.OrgID, code.CodeHash, nilIfEmpty(code.CreatedBy),
		code.Status, code.ExpiresAt, code.CreatedAt)
	return err
}

// GetDeviceClaimCodeByHash reads a claim code by its hash. Deliberately no
// FOR UPDATE: the control-plane routing lookup is read-only, and single-use
// safety comes from MarkDeviceClaimCodeUsed's atomic conditional update plus
// the per-org advisory quota lock.
func (s *PostgresStore) GetDeviceClaimCodeByHash(ctx context.Context, hash string) (*business.DeviceClaimCode, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT `+deviceClaimCodeColumns+` FROM device_claim_codes WHERE code_hash = $1`, hash)
	var code business.DeviceClaimCode
	var createdBy, usedByDeviceID *string
	err := row.Scan(
		&code.ID,
		&code.OrgID,
		&code.CodeHash,
		&createdBy,
		&code.Status,
		&code.ExpiresAt,
		&code.CreatedAt,
		&code.UsedAt,
		&usedByDeviceID,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if createdBy != nil {
		code.CreatedBy = *createdBy
	}
	if usedByDeviceID != nil {
		code.UsedByDeviceID = *usedByDeviceID
	}
	return &code, nil
}

// MarkDeviceClaimCodeUsed consumes a pending code. Returns false when the
// code was already consumed/expired by a concurrent transaction.
func (s *PostgresStore) MarkDeviceClaimCodeUsed(ctx context.Context, codeID, deviceID string) (bool, error) {
	result, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE device_claim_codes
		SET status = 'used', used_at = CURRENT_TIMESTAMP, used_by_device_id = $2
		WHERE id = $1 AND status = 'pending' AND expires_at > CURRENT_TIMESTAMP`,
		codeID, deviceID)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *PostgresStore) ExpirePendingDeviceClaimCodes(ctx context.Context, orgID string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE device_claim_codes
		SET status = 'expired'
		WHERE org_id = $1 AND status = 'pending' AND expires_at <= CURRENT_TIMESTAMP`,
		orgID)
	return err
}

func scanDevice(row pgx.Row) (*business.Device, error) {
	var device business.Device
	var createdBy *string
	err := row.Scan(
		&device.ID,
		&device.OrgID,
		&device.DevicePublicKey,
		&device.Name,
		&createdBy,
		&device.CreatedAt,
		&device.RevokedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if createdBy != nil {
		device.CreatedBy = *createdBy
	}
	return &device, nil
}
