package infra

import (
	"context"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

const connectorCredentialColumns = `
	id::text, org_id::text, source_id::text, provider, secret_encrypted,
	created_at, updated_at`

func scanConnectorCredential(row pgx.Row) (*business.ConnectorCredential, error) {
	var c business.ConnectorCredential
	if err := row.Scan(
		&c.ID, &c.OrgID, &c.SourceID, &c.Provider, &c.SecretEncrypted,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertConnectorCredential writes a source's encrypted credential. One row per
// source (UNIQUE(source_id)); re-storing keeps the original id and the source's
// org — a source never migrates orgs. Runs under the caller's WithOrgTx.
func (s *PostgresStore) UpsertConnectorCredential(ctx context.Context, credential *business.ConnectorCredential) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO connector_credentials (id, org_id, source_id, provider, secret_encrypted)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (source_id) DO UPDATE SET
			provider         = EXCLUDED.provider,
			secret_encrypted = EXCLUDED.secret_encrypted,
			updated_at       = NOW()`,
		credential.ID, credential.OrgID, credential.SourceID,
		credential.Provider, credential.SecretEncrypted,
	)
	return err
}

// GetConnectorCredential returns the credential for a source, or (nil, nil) when
// none is visible. Under WithOrgTx RLS scopes it to the caller's org; under
// WithControlPlane it serves the cross-tenant webhook-signing-secret lookup.
func (s *PostgresStore) GetConnectorCredential(ctx context.Context, sourceID string) (*business.ConnectorCredential, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT `+connectorCredentialColumns+` FROM connector_credentials WHERE source_id = $1`, sourceID)
	credential, err := scanConnectorCredential(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return credential, nil
}

// DeleteConnectorCredential removes a source's credential. Runs under the
// caller's WithOrgTx, so RLS confines the delete to the caller's org.
func (s *PostgresStore) DeleteConnectorCredential(ctx context.Context, sourceID string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx,
		`DELETE FROM connector_credentials WHERE source_id = $1`, sourceID)
	return err
}
