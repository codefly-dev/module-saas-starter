package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra/internal/txbind"
	"accounts/pkg/jobs"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) InstallationStepLeaseValid(ctx context.Context, lease *jobsv1.JobLeaseReference) (bool, error) {
	var valid bool
	// The gateway's headless consumer has no tenant context; this fixed-queue
	// definer checks only its exact lease and exposes no job-table read access.
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT public.installation_step_lease($1,$2,$3)`, lease.GetJobId(), lease.GetWorkerId(), lease.GetLeaseToken()).Scan(&valid)
	})
	return valid, err
}

func (s *PostgresStore) WithInstallationStepAuthority(ctx context.Context, lease *jobsv1.JobLeaseReference, envelope uint64, use func(context.Context, *business.InstallationStepAuthority) error) error {
	// Setup resolves the target and registry across tenants from a verified
	// lease, then holds that installation's authority through signing.
	return s.WithControlPlane(ctx, func(ctx context.Context) error {
		executor := s.getQueryExecutor(ctx)
		// The queue-specific lease check never grants table access to this pool.
		if err := checkInstallationEnvelope(ctx, executor, envelope); err != nil {
			return err
		}
		var valid bool
		err := executor.QueryRow(ctx, `SELECT public.installation_step_lease($1,$2,$3)`, lease.GetJobId(), lease.GetWorkerId(), lease.GetLeaseToken()).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return errors.New("installation step lease expired")
		}
		if _, err := executor.Exec(ctx, `SELECT 1 FROM solution_targets WHERE id IN
		 (SELECT target_id FROM installations WHERE install_job_id=$1) ORDER BY id FOR SHARE`, lease.GetJobId()); err != nil {
			return err
		}
		a := &business.InstallationStepAuthority{}
		var raw []byte
		err = executor.QueryRow(ctx, `SELECT i.id::text,i.org_id::text,i.revision,i.target_id::text,
		 t.solution_id,i.registry_binding_id,i.registry_principal_id::text,i.root_scope_node_id::text,i.scope
		 FROM installations i JOIN solution_targets t ON t.id=i.target_id
		 WHERE i.install_job_id=$1 AND i.status IN ('pending','active') AND t.closed_generation IS NULL
		 FOR SHARE OF i`, lease.GetJobId()).Scan(&a.InstallationID, &a.OrganizationID, &a.Revision, &a.TargetID, &a.RouteAlias, &a.BindingID, &a.PrincipalID, &a.RootID, &raw)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &a.Scope); err != nil {
			return err
		}
		a.Facts, err = resolveInstallationAuthority(ctx, executor, a.OrganizationID, a.InstallationID, nil, true)
		if err != nil {
			return err
		}
		rows, err := executor.Query(ctx, `SELECT DISTINCT rp.resource,rp.action FROM scope_grants g
		 JOIN role_permissions rp ON rp.role_id=g.role_id JOIN scope_nodes n ON n.id=$3
		 WHERE g.org_id=$1 AND g.subject_kind='principal' AND g.subject_id=$2
		 AND g.scope_path @> n.scope_path AND (g.expires_at IS NULL OR g.expires_at>now())
		 ORDER BY rp.resource,rp.action`, a.OrganizationID, a.Facts.Actor.ID, a.RootID)
		if err != nil {
			return err
		}
		permissions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (business.WorkContextPermission, error) {
			var p business.WorkContextPermission
			err := row.Scan(&p.ResourceKind, &p.Action)
			return p, err
		})
		if err != nil {
			return err
		}
		if len(permissions) == 0 {
			return errors.New("installation standing grant is absent")
		}
		a.StandingPermissions = permissions
		return use(ctx, a)
	})
}

// EnqueueInstallationStep attaches the outbox job in the lifecycle transaction.
// The row lock fences retries and revision changes; no credential is persisted.
func (s *PostgresStore) EnqueueInstallationStep(ctx context.Context, orgID, id, run string) error {
	executor := s.getQueryExecutor(ctx)
	var revision uint64
	if err := executor.QueryRow(ctx, `SELECT revision FROM installations
	 WHERE org_id=$1 AND id=$2 AND status IN ('pending','active') FOR UPDATE`, orgID, id).Scan(&revision); err != nil {
		return err
	}
	req, err := business.NewInstallationStepJob(business.InstallationStep{InstallationID: id, OrganizationID: orgID, Revision: revision}, run)
	if err != nil {
		return err
	}
	job, err := insertInstallationJob(ctx, executor, orgID, req)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, `UPDATE installations SET install_job_id=$3, install_attempts=0 WHERE org_id=$1 AND id=$2`, orgID, id, job.GetJobId())
	return err
}

// Both host installation paths reuse the same transactional outbox producer.
// Their callers hold the applicable canonical or module installation lock.
func insertInstallationJob(ctx context.Context, executor QueryExecutor, orgID string, req *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	bound, ok := txbind.Lookup(ctx)
	if !ok {
		return nil, jobs.ErrTransactionRequired
	}
	if bound.Pool != txbind.ControlPlane && (bound.Pool != txbind.Request || bound.OrgID != orgID) {
		return nil, errors.New("installation enqueue requires its organization's transaction or the control plane")
	}
	prepared, err := prepareJobEnqueue(req)
	if err != nil {
		return nil, err
	}
	// Setup is host work for pending consent, not an active module's enqueue.
	// The installation lock above is its authority and survives through commit.
	// Like the webhook relay, use the dedicated enqueue_job_message path with
	// tenant scope on the existing transaction. The control-plane login cannot
	// assume app_tenant; its declared job-worker role can enqueue tenant work.
	// No callback or product-table access runs while that role is selected.
	if bound.Pool == txbind.ControlPlane {
		if _, err := executor.Exec(ctx, "SET LOCAL ROLE app_job_worker"); err != nil {
			return nil, err
		}
		if err := bindRequestScope(ctx, bound.Tx, orgID, ""); err != nil {
			return nil, err
		}
	}
	job, err := insertPreparedJob(ctx, executor, prepared)
	if err != nil {
		return nil, err
	}
	if bound.Pool == txbind.ControlPlane {
		if err := bindRequestScope(ctx, bound.Tx, "", ""); err != nil {
			return nil, err
		}
		if _, err := executor.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole); err != nil {
			return nil, err
		}
	}
	return job, nil
}

func (s *PostgresStore) RetryInstallation(ctx context.Context, orgID, id string) (*gen.Installation, uint64, error) {
	// Match target withdrawal's target -> installation lock order.
	executor := s.getQueryExecutor(ctx)
	if _, err := executor.Exec(ctx, `SELECT 1 FROM solution_targets WHERE id IN
	 (SELECT target_id FROM installations WHERE org_id=$1 AND id=$2) FOR SHARE`, orgID, id); err != nil {
		return nil, 0, err
	}
	i, err := scanInstallation(executor.QueryRow(ctx, `UPDATE installations
	 SET status='pending', failure_reason=NULL, install_attempts=0
	 WHERE org_id=$1 AND id=$2 AND status='failed' RETURNING `+installationColumns, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, business.NewStoreError(fmt.Errorf("only a failed installation may be retried"), business.ErrTypeConflict)
	}
	if err != nil {
		return nil, 0, err
	}
	if err := s.EnqueueInstallationStep(ctx, orgID, id, business.NewIDString()); err != nil {
		return nil, 0, err
	}
	var revision uint64
	if err := executor.QueryRow(ctx, `SELECT revision FROM installations WHERE org_id=$1 AND id=$2`, orgID, id).Scan(&revision); err != nil {
		return nil, 0, err
	}
	return i, revision, nil
}
