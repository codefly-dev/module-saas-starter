package infra

import (
	"accounts/pkg/business"
	"accounts/pkg/jobs"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	ctx, release, err := s.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	use := ctx.Value(moduleAuthorityUseKey{}).(*moduleAuthorityUse)
	if !use.locked {
		recordControlPlane(ctx)
		if _, err := use.tx.Exec(ctx, "SET LOCAL ROLE app_control_plane"); err != nil {
			return nil, err
		}
		if _, err := use.tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('module-envelope-installation',0))`); err != nil {
			return nil, err
		}
		use.locked = true
	}
	tx := use.tx
	err = func() error {
		var scope []byte
		var revision, epoch, envelope uint64
		// Lock order is target, then installation — the order target withdrawal
		// takes (solution_targets FOR UPDATE, then its installations), so a
		// capability check and a withdrawal can never wait on each other in a
		// cycle. Targets are locked first, in id order, in their own statement.
		if _, err := tx.Exec(ctx, `
   SELECT 1 FROM public.solution_targets t
   WHERE t.id IN (SELECT i.target_id FROM public.installations i
     WHERE i.org_id = $1::uuid AND i.registry_principal_id = $2::uuid
       AND ($3 = '' OR i.id::text = $3) AND i.status = 'active')
   ORDER BY t.id FOR SHARE`, orgID, principalID, installationID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
   SELECT i.id::text, i.revision, i.registry_binding_id, i.scope,
      COALESCE((SELECT revision FROM public.principal_authorization_revisions
        WHERE org_id = i.org_id AND principal_id = i.registry_principal_id), 0),
      COALESCE((SELECT max(revision) FROM public.module_envelopes), 0),
      COALESCE((SELECT applied_generation FROM public.solution_host_bindings
        WHERE binding_id = i.registry_binding_id AND NOT applied_removed), 0)
   FROM public.installations i
   JOIN public.solution_targets t ON t.id = i.target_id AND t.binding_id = i.registry_binding_id
   WHERE i.org_id = $1::uuid AND i.registry_principal_id = $2::uuid
     AND ($3 = '' OR i.id::text = $3) AND i.status = 'active'
     AND t.closed_generation IS NULL FOR SHARE OF i`, orgID, principalID, installationID,
		).Scan(&live.InstallationID, &revision, &live.RegistryBindingID, &scope, &epoch, &envelope, &live.AppliedManifestGeneration)
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
	}()
	if err != nil {
		return nil, err
	}
	use.installations[live.InstallationID] = jobs.ModuleAuthority{PrincipalID: principalID, OrganizationID: orgID, InstallationID: live.InstallationID, InstallationRevision: live.InstallationRevision, BindingID: live.RegistryBindingID}
	return live, nil
}

var _ business.ModuleAuthorityStore = (*PostgresStore)(nil)

// This transaction holds locks only. It is deliberately not installed as the
// ambient store transaction: an effect still uses its own tenant/worker role.
// The shared envelope lock follows the envelope writer's lock order, before
// installation/target row locks. Narrowing, uninstall and tenancy replacement
// must wait until the capability's effect has committed and release is called.
type moduleAuthorityUseKey struct{}
type moduleAuthorityUse struct {
	closed        bool
	locked        bool
	tx            pgx.Tx
	installations map[string]jobs.ModuleAuthority
}

func (s *PostgresStore) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	if use, ok := ctx.Value(moduleAuthorityUseKey{}).(*moduleAuthorityUse); ok {
		if use.closed {
			return ctx, nil, fmt.Errorf("module authority use already released")
		}
		return ctx, func() {}, nil
	}
	// Reserve the fence connection before downstream transactions. Acquiring
	// it while already holding an effect connection can deadlock two full
	// pools against each other. Authority locks and the role are still taken
	// only when the live read is reached for a known principal.
	tx, err := s.authorityUse.Begin(ctx)
	if err != nil {
		return ctx, nil, err
	}
	use := &moduleAuthorityUse{tx: tx, installations: map[string]jobs.ModuleAuthority{}}
	release := func() {
		use.closed = true
		clear(use.installations)
		if use.tx == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = use.tx.Rollback(cleanup)
	}
	return context.WithValue(ctx, moduleAuthorityUseKey{}, use), release, nil
}

// Do not take the same row lock on a second connection while a lifecycle
// writer waits for our outer lock: PostgreSQL's lock queue can put that second
// SHARE behind the writer, deadlocking the operation with its own fence.
// Only the exact stamp already locked by this request can reuse the fence.
func lockModuleJobAuthority(ctx context.Context, executor QueryExecutor, stamp []byte) error {
	if use, ok := ctx.Value(moduleAuthorityUseKey{}).(*moduleAuthorityUse); ok && !use.closed {
		var attributes map[string]string
		if err := json.Unmarshal(stamp, &attributes); err != nil {
			return err
		}
		if held, ok := use.installations[attributes["codefly.installation_id"]]; ok {
			matches := true
			for key, value := range held.Attributes() {
				if attributes[key] != value {
					matches = false
					break
				}
			}
			if matches {
				return nil
			}
		}
	}
	_, err := executor.Exec(ctx, `SELECT public.lock_module_job_authority($1::jsonb)`, stamp)
	return err
}
