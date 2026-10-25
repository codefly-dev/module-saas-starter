package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// WorkContextInstallationIdentity answers the organization, delivered principal
// and status of the solution installation id names, on the seal reads'
// control-plane snapshot. The id is compared as text, so an id that is not a
// uuid is an installation that does not exist rather than a malformed query.
func (s *PostgresStore) WorkContextInstallationIdentity(ctx context.Context, id string) (business.InstallationIdentity, error) {
	var out business.InstallationIdentity
	err := s.sealRead(ctx, func(ctx context.Context, q QueryExecutor) error {
		return q.QueryRow(ctx, `SELECT org_id::text, COALESCE(registry_principal_id::text, ''), status
			FROM public.installations WHERE id::text = $1 AND kind = 'solution'`, id).Scan(&out.OrgID, &out.PrincipalID, &out.Status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return business.InstallationIdentity{}, business.NewStoreError(fmt.Errorf("installation %q does not exist", id), business.ErrTypeNotFound)
	}
	return out, err
}
