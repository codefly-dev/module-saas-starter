package infra

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/jackc/pgx/v5"
)

// CanonicalSolutionSummaries projects the canonical row into the host's existing
// listing. It creates no scope root, standing grant, person grant or mirrored
// installation. The receipt retains the immutable host target across withdrawal.
func (s *PostgresStore) CanonicalSolutionSummaries(ctx context.Context, org, id, after string, status gen.InstallationStatus, limit int) ([]*gen.InstallationSummary, error) {
	var out []*gen.InstallationSummary
	plans := map[string]*business.SolutionInstallationPlan{}
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		rows, err := s.getQueryExecutor(ctx).Query(ctx, `SELECT i.id,i.organization_id::text,i.principal_id,i.installed_by,i.status,i.revision,i.consent,
 COALESCE(t.closed_generation IS NULL AND NOT b.applied_removed
 AND b.applied_generation=m.generation AND m.principal=i.consent->'host_plan'->'Approval'->>'TargetID'
 AND m.generation::text=i.consent->'host_plan'->'Approval'->>'Generation'
 AND b.applied_digest=i.consent->'host_plan'->>'DeliveryDigest',false)
 FROM public.authority_core_installations i
 LEFT JOIN public.solution_targets t ON t.id::text=i.consent->'host_plan'->>'HostTargetID'
 LEFT JOIN public.solution_host_bindings b ON b.binding_id=t.binding_id
 LEFT JOIN public.authority_core_modules m ON m.name=i.target
 WHERE i.organization_id=$1::uuid AND ($2='' OR i.id=$2) AND ($3='' OR i.id>$3)
 AND ($4='' OR i.status=$4) ORDER BY i.id LIMIT $5`, org, id, after, installationStatusColumn(status), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			i := &gen.Installation{}
			var state string
			var raw []byte
			var current bool
			if err := rows.Scan(&i.Id, &i.OrgId, &i.AgentPrincipalId, &i.OwnerPrincipalId, &state, &i.Revision, &raw, &current); err != nil {
				return err
			}
			var receipt solutionConsent
			if err := json.Unmarshal(raw, &receipt); err != nil {
				return err
			}
			if receipt.Plan != nil {
				i.TargetId = receipt.Plan.HostTargetID
				if receipt.Plan.Receipt == receipt.ConsentReceipt && receipt.Plan.Revision == i.Revision {
					plans[i.Id] = receipt.Plan
				}
			}
			i.Status = map[string]gen.InstallationStatus{"pending": gen.InstallationStatus_INSTALLATION_STATUS_PENDING,
				"active": gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, "revoked": gen.InstallationStatus_INSTALLATION_STATUS_REVOKED}[state]
			health := gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED
			if state == "active" && current {
				health = gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY
			}
			out = append(out, &gen.InstallationSummary{Installation: i, Health: health})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for _, summary := range out {
			if summary.Installation.Status == gen.InstallationStatus_INSTALLATION_STATUS_PENDING {
				if err := s.projectCanonicalSetup(ctx, summary.Installation, plans[summary.Installation.Id]); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return out, err
}

// Read Jobs under its existing worker role, with every attempt association.
// Only the receiver's explicitly public refusal is shown; retry diagnostics
// stay private. This projection never changes the canonical pending state.
func (s *PostgresStore) projectCanonicalSetup(ctx context.Context, i *gen.Installation, plan *business.SolutionInstallationPlan) (err error) {
	i.SetupState, i.SetupMessage = "unavailable", "Setup has no matching queued attempt. Resume the original reviewed installation."
	if plan == nil || plan.InstallationID != i.Id || plan.Organization != i.OrgId || plan.SetupJobID == "" {
		return nil
	}
	state, code, reason, err := readCanonicalSetupJob(ctx, s.getQueryExecutor(ctx), domain.Installation{ID: i.Id, Organization: i.OrgId, Revision: i.Revision, Consent: plan.Receipt}, plan.SetupJobID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
		return nil
	}
	if err != nil {
		return err
	}
	i.SetupState = state
	switch state {
	case "pending":
		i.SetupMessage = "Setup is queued."
	case "processing":
		i.SetupMessage = "Setup is running."
	case "retrying":
		i.SetupMessage = "Setup is temporarily unavailable and will retry."
	case "succeeded":
		i.SetupMessage = "Setup acknowledged. Continue the original reviewed installation to finish activation."
	case "canceled":
		i.SetupMessage = "Setup was canceled. Review the installation again."
	case "dead_letter":
		i.SetupMessage = "Setup did not complete. Correct the problem, then retry the original reviewed installation."
		if code == "installation.refused" {
			i.FailureReason = business.BoundedInstallationReason(reason)
			i.SetupMessage += " " + i.FailureReason
		}
	}
	return nil
}
