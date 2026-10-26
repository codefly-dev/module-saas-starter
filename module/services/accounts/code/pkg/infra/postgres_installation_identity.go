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

// WorkContextAgentInstallation answers the active installation whose agent is
// agentPrincipalID in orgID, on the same snapshot. An agent is created for
// one installation; two active ones naming it answer neither.
func (s *PostgresStore) WorkContextAgentInstallation(ctx context.Context, orgID, agentPrincipalID string) (string, error) {
	var ids []string
	err := s.sealRead(ctx, func(ctx context.Context, q QueryExecutor) error {
		return q.QueryRow(ctx, `SELECT COALESCE(array_agg(id::text ORDER BY id), ARRAY[]::text[])
			FROM public.installations
			WHERE org_id::text = $1 AND agent_principal_id::text = $2 AND status = 'active'`, orgID, agentPrincipalID).Scan(&ids)
	})
	if err != nil {
		return "", fmt.Errorf("read the agent's installation: %w", err)
	}
	if len(ids) != 1 {
		return "", business.NewStoreError(fmt.Errorf("agent %s is the agent of %d active installations in organization %s, not one", agentPrincipalID, len(ids), orgID), business.ErrTypeNotFound)
	}
	return ids[0], nil
}
