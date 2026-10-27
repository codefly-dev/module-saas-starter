package infra

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type installationApprovalReceipt struct {
	OperationID string
	Fingerprint string
	Revision    uint64
}

// InstallSolution fences consent in the SAME transaction as its scope write.
// Reuse the installation lock, lifecycle revision and scoped tenant row. A
// receipt is meaningful only while its resulting revision is still current.
func (s *PostgresStore) InstallSolution(ctx context.Context, p *business.InstallSolutionParams) (*gen.Installation, error) {
	if p.Approval == nil {
		return s.installSolution(ctx, p)
	} // internal bounded/fixture creation
	a := p.Approval
	if err := a.Validate(); err != nil {
		return nil, err
	}
	executor := s.getQueryExecutor(ctx)
	admin, err := isCurrentOrgAdmin(ctx, executor, p.OrgID, p.GrantedBy)
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, status.Error(codes.PermissionDenied, "INSTALLATION_APPROVAL_ADMIN_REQUIRED: only a current organization administrator may approve installation authority")
	}
	if err := checkInstallationEnvelope(ctx, executor, p.EnvelopeRevision); err != nil {
		return nil, err
	}
	if err := lockSolutionInstallation(ctx, executor, p.OrgID, p.TargetID); err != nil {
		return nil, err
	}
	var generation uint64
	if err := executor.QueryRow(ctx, `SELECT public.lock_installation_approval_target($1::uuid)`, p.TargetID).Scan(&generation); err != nil {
		return nil, err
	}
	if a.TargetID != p.TargetID || a.Generation != generation {
		return nil, business.InstallationApprovalRefusal("INSTALLATION_APPROVAL_GENERATION_CHANGED", "the reviewed target or generation is no longer current; review ListAvailableSolutions and approve again")
	}
	// Business binds the complete caller intent before resolving defaults.
	fingerprint := a.Intent()
	if fingerprint == "" {
		return nil, business.InstallationApprovalRefusal("INSTALLATION_APPROVAL_REQUIRED", "approval intent was not bound by the installation service")
	}
	existing, err := scanInstallation(executor.QueryRow(ctx, `SELECT `+installationColumns+` FROM public.installations WHERE org_id=$1 AND target_id=$2 ORDER BY (status <> 'revoked') DESC, revision DESC, id DESC LIMIT 1 FOR UPDATE`, p.OrgID, p.TargetID))
	revision := uint64(0)
	if err == nil {
		revision = existing.Revision
		var receiptRaw []byte
		if err := executor.QueryRow(ctx, `SELECT consent_receipt FROM public.installations WHERE id=$1`, existing.Id).Scan(&receiptRaw); err != nil {
			return nil, err
		}
		var receipt installationApprovalReceipt
		if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
			return nil, err
		}
		if receipt.OperationID == a.OperationID {
			if receipt.Fingerprint != fingerprint {
				return nil, business.InstallationApprovalRefusal("INSTALLATION_APPROVAL_OPERATION_CONFLICT", "operation_id was already used for different approval inputs")
			}
			if existing.Status == gen.InstallationStatus_INSTALLATION_STATUS_REVOKED || receipt.Revision != revision {
				return nil, business.InstallationApprovalRefusal("INSTALLATION_APPROVAL_REVISION_CHANGED", "installation changed after this approval; review its current revision and approve again")
			}
			p.ApprovalReplayed = true
			return existing, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if a.ExpectedRevision != revision {
		return nil, business.InstallationApprovalRefusal("INSTALLATION_APPROVAL_REVISION_CHANGED", "the reviewed installation revision is no longer current; review the installation listing and approve again")
	}
	installed, err := s.installSolution(ctx, p)
	if err != nil {
		return nil, err
	}
	// Every fresh receipt advances the lifecycle revision, even if the scope
	// did not change. Exact retries returned above never write another receipt.
	if err := executor.QueryRow(ctx, `SELECT revision FROM public.installations WHERE id=$1`, installed.Id).Scan(&installed.Revision); err != nil {
		return nil, err
	}
	receipt, err := json.Marshal(installationApprovalReceipt{a.OperationID, fingerprint, installed.Revision + 1})
	if err != nil {
		return nil, err
	}
	if err := executor.QueryRow(ctx, `UPDATE public.installations SET consent_receipt=$2::jsonb WHERE id=$1 RETURNING revision`, installed.Id, receipt).Scan(&installed.Revision); err != nil {
		return nil, err
	}
	// The revision trigger cancels a superseded attempt and fences its lease.
	// Attach the replacement only after its final receipt revision is known.
	if installed.Status == gen.InstallationStatus_INSTALLATION_STATUS_PENDING {
		if err := s.EnqueueInstallationStep(ctx, p.OrgID, installed.Id, a.OperationID); err != nil {
			return nil, err
		}
	}
	return installed, nil
}
