package infra

import (
	"accounts/pkg/business"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// LiveModuleAuthority resolves consent by the registry principal, never the
// per-installation agent. The org, principal, optional seal, presence liveness,
// installation revision/scope and committed envelope are one SQL decision.
func (s *PostgresStore) LiveModuleAuthority(ctx context.Context, principalID, orgID, installationID string) (*business.LiveModuleAuthority, error) {
	if orgID == "" {
		return nil, fmt.Errorf("%w: no bound organization", business.ErrModuleAuthorityUnreadable)
	}
	live := &business.LiveModuleAuthority{}
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		var scope []byte
		var revision, epoch, envelope uint64
		err := s.getQueryExecutor(ctx).QueryRow(ctx, `
   SELECT i.id::text, i.revision, i.registry_binding_id, i.scope,
      COALESCE((SELECT revision FROM public.principal_authorization_revisions
        WHERE org_id = i.org_id AND principal_id = i.registry_principal_id), 0),
      COALESCE((SELECT max(revision) FROM public.module_envelopes), 0)
   FROM public.installations i
   JOIN public.solution_targets t ON t.id = i.target_id AND t.binding_id = i.registry_binding_id
   WHERE i.org_id = $1::uuid AND i.registry_principal_id = $2::uuid
     AND ($3 = '' OR i.id::text = $3) AND i.status = 'active'
     AND t.closed_generation IS NULL`, orgID, principalID, installationID,
		).Scan(&live.InstallationID, &revision, &live.RegistryBindingID, &scope, &epoch, &envelope)
		if errors.Is(err, pgx.ErrNoRows) {
			return business.ErrModuleInstallationInactive
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(scope, &live.Scope); err != nil {
			return err
		}
		live.InstallationRevision, live.ProducerEpoch, live.EnvelopeRevision = revision, epoch, &envelope
		return nil
	})
	if err != nil {
		return nil, err
	}
	return live, nil
}

var _ business.ModuleAuthorityStore = (*PostgresStore)(nil)
