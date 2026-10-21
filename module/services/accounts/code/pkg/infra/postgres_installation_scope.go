package infra

import (
	"accounts/pkg/business"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) InstallationAuthorityState(ctx context.Context, orgID, id string) (*business.InstallationAuthorityState, error) {
	state := &business.InstallationAuthorityState{}
	var raw []byte
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT kind, COALESCE(registry_principal_id::text,''),
        registry_binding_id, revision, scope FROM public.installations
        WHERE id=$1::uuid AND org_id=$2::uuid AND status='active'`, id, orgID).Scan(
		&state.Kind, &state.PrincipalID, &state.BindingID, &state.Revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.NewStoreError(business.ErrModuleInstallationInactive, business.ErrTypeNotFound)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &state.Scope); err != nil {
		return nil, err
	}
	return state, nil
}

// Share the envelope writer's lock: an installation cannot commit against a
// ceiling replaced between validation and insertion. The writer takes EXCLUSIVE.
func checkInstallationEnvelope(ctx context.Context, executor QueryExecutor, revision uint64) error {
	_, err := executor.Exec(ctx, `SELECT public.check_installation_envelope($1::bigint)`, revision)
	return err
}

func (s *PostgresStore) NarrowInstallationScope(ctx context.Context, orgID, id string, revision uint64, scope business.InstallationScope, registry business.ModulePrincipalRegistry, envelope uint64) error {
	executor := s.getQueryExecutor(ctx)
	if err := checkInstallationEnvelope(ctx, executor, envelope); err != nil {
		return err
	}
	var raw []byte
	var principal, binding string
	var current uint64
	if err := executor.QueryRow(ctx, `SELECT scope, registry_principal_id::text, registry_binding_id, revision
    FROM public.installations WHERE id=$1::uuid AND org_id=$2::uuid AND status='active' FOR UPDATE`, id, orgID).Scan(&raw, &principal, &binding, &current); err != nil {
		return err
	}
	if current != revision {
		return business.NewStoreError(fmt.Errorf("installation revision changed: expected %d, current %d", revision, current), business.ErrTypeConflict)
	}
	var old business.InstallationScope
	if err := json.Unmarshal(raw, &old); err != nil {
		return err
	}
	grant, found := registry[principal]
	if !found || grant.PresenceBinding != binding || !business.InstallationScopeWithin(scope, business.FullInstallationScope(grant)) || !business.InstallationScopeWithin(scope, business.ResolveInstallationScope(old, business.FullInstallationScope(grant))) {
		return business.NewStoreError(fmt.Errorf("scope must narrow this installation within the current ceiling"), business.ErrTypeValidation)
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	// The trigger owns revision increments and cancellation for ALL writers,
	// including target withdrawal and organization deletion.
	tag, err := executor.Exec(ctx, `UPDATE public.installations SET scope=$3::jsonb WHERE id=$1::uuid AND org_id=$2::uuid AND scope IS DISTINCT FROM $3::jsonb`, id, orgID, encoded)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	var kind string
	if err := executor.QueryRow(ctx, `SELECT kind FROM installations WHERE id=$1 AND org_id=$2`, id, orgID).Scan(&kind); err != nil {
		return err
	}
	if kind == "module" {
		return nil
	}
	return s.EnqueueInstallationStep(ctx, orgID, id, "narrow")
}
