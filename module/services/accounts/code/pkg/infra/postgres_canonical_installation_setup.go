package infra

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

func (s *PostgresStore) SolutionInstallationPlan(ctx context.Context, org, id string) (business.SolutionInstallationPlan, error) {
	var plan business.SolutionInstallationPlan
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		var raw []byte
		var revision uint64
		if err := s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT consent,revision FROM public.authority_core_installations
 WHERE id=$1 AND organization_id=$2::uuid AND status IN ('pending','active')`, id, org).Scan(&raw, &revision); err != nil {
			return err
		}
		var receipt solutionConsent
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		if receipt.Plan == nil || receipt.Plan.Receipt != receipt.ConsentReceipt || receipt.Plan.Revision != revision {
			return domain.NotNow("INSTALLATION_PLAN_CHANGED: the pending attempt has no matching reviewed plan")
		}
		plan = *receipt.Plan
		return nil
	})
	return plan, err
}

func (s *PostgresStore) ModuleInstallationPrerequisite(ctx context.Context, org, id string, envelope uint64) (business.ModuleInstallationPrerequisite, error) {
	var witness business.ModuleInstallationPrerequisite
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		q := s.getQueryExecutor(ctx)
		if err := checkInstallationEnvelope(ctx, q, envelope); err != nil {
			return err
		}
		var err error
		witness, err = (coreTx{ctx: ctx, q: q}).modulePrerequisite(id, org, envelope)
		return err
	})
	return witness, err
}

func (s *PostgresStore) RecordSolutionPrerequisites(ctx context.Context, inst domain.Installation, dependencies []business.ModuleInstallationPrerequisite) error {
	return s.WithControlPlane(ctx, func(ctx context.Context) error {
		q := s.getQueryExecutor(ctx)
		var raw []byte
		if err := q.QueryRow(ctx, `SELECT consent FROM public.authority_core_installations WHERE id=$1 AND organization_id=$2::uuid AND revision=$3 AND status='pending' FOR UPDATE`, inst.ID, inst.Organization, inst.Revision).Scan(&raw); err != nil {
			if err == pgx.ErrNoRows {
				return domain.NotNow("INSTALLATION_APPROVAL_REVISION_CHANGED: the setup attempt was superseded or revoked")
			}
			return err
		}
		var receipt solutionConsent
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		if receipt.Plan == nil || receipt.ConsentReceipt != inst.Consent || receipt.Plan.Receipt != inst.Consent {
			return domain.NotNow("the reviewed installation changed while recording prerequisites")
		}
		receipt.Plan.Dependencies = dependencies
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `UPDATE public.authority_core_installations SET consent=$2::jsonb WHERE id=$1`, inst.ID, raw)
		return err
	})
}

func (s *PostgresStore) EnqueueCanonicalInstallationStep(ctx context.Context, inst domain.Installation, retry bool) (string, error) {
	var jobID string
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		q := s.getQueryExecutor(ctx)
		var raw []byte
		var revision uint64
		if err := q.QueryRow(ctx, `SELECT consent,revision FROM public.authority_core_installations
 WHERE id=$1 AND organization_id=$2::uuid AND status='pending' FOR UPDATE`, inst.ID, inst.Organization).Scan(&raw, &revision); err != nil {
			return err
		}
		var receipt solutionConsent
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		if receipt.Plan == nil || receipt.ConsentReceipt != inst.Consent || revision != inst.Revision || receipt.Plan.Receipt != inst.Consent {
			return domain.NotNow("INSTALLATION_PLAN_CHANGED: setup does not match the pending attempt")
		}
		if receipt.Plan.SetupJobID != "" {
			jobID = receipt.Plan.SetupJobID
			if retry {
				state, _, _, err := readCanonicalSetupJob(ctx, q, inst, jobID)
				if err != nil {
					return err
				}
				if _, err := q.Exec(ctx, "SET LOCAL ROLE app_job_worker"); err != nil {
					return err
				}
				if state == "dead_letter" {
					replayed, err := replayJob(ctx, q, &jobsv1.ReplayJobRequest{SourceJobId: jobID, IdempotencyKey: "installation-retry:" + jobID})
					if err != nil {
						return err
					}
					jobID, receipt.Plan.SetupJobID = replayed.GetJobId(), replayed.GetJobId()
				}
				if _, err := q.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole); err != nil {
					return err
				}
				raw, err := json.Marshal(receipt)
				if err != nil {
					return err
				}
				_, err = q.Exec(ctx, `UPDATE public.authority_core_installations SET consent=$2::jsonb WHERE id=$1`, inst.ID, raw)
				return err
			}
			return nil
		}
		req, err := business.NewInstallationStepJob(business.InstallationStep{InstallationID: inst.ID, OrganizationID: inst.Organization, Revision: inst.Revision}, inst.Consent.OperationID)
		if err != nil {
			return err
		}
		req.Job.Attributes["canonical.installation"] = "true"
		req.Job.Attributes["canonical.operation"] = inst.Consent.OperationID
		req.Job.Attributes["canonical.intent"] = inst.Consent.Intent
		job, err := insertInstallationJob(ctx, q, inst.Organization, req)
		if err != nil {
			return err
		}
		jobID, receipt.Plan.SetupJobID = job.GetJobId(), job.GetJobId()
		raw, err = json.Marshal(receipt)
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `UPDATE public.authority_core_installations SET consent=$2::jsonb WHERE id=$1`, inst.ID, raw)
		return err
	})
	return jobID, err
}

// All canonical job consumers use this exact association inside their caller's
// transaction. The share lock survives role restoration and fences final ACK.
func readCanonicalSetupJob(ctx context.Context, q QueryExecutor, inst domain.Installation, jobID string) (state, code, reason string, err error) {
	if jobID == "" {
		return "", "", "", pgx.ErrNoRows
	}
	if _, err = q.Exec(ctx, "SET LOCAL ROLE app_job_worker"); err != nil {
		return
	}
	defer func() {
		_, restore := q.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole)
		if restore != nil {
			err = restore
		}
	}()
	err = q.QueryRow(ctx, `SELECT state,COALESCE(last_error_code,''),COALESCE(last_error_message,'') FROM public.job_messages
 WHERE id=$1::uuid AND organization_id=$2::uuid AND queue='installation-step' AND source='saas.installation'
 AND topic='installation.step' AND scope_kind='tenant' AND attributes->>'canonical.installation'='true'
 AND attributes->>'codefly.installation_id'=$3 AND attributes->>'codefly.installation_revision'=$4
 AND attributes->>'canonical.operation'=$5 AND attributes->>'canonical.intent'=$6 FOR SHARE`, jobID, inst.Organization, inst.ID,
		fmt.Sprint(inst.Revision), inst.Consent.OperationID, inst.Consent.Intent).Scan(&state, &code, &reason)
	return
}
