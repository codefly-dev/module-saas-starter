package business

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InstallationApproval is the administrator's reviewed offer, never a default
// resolved at execution time. The durable receipt makes unchanged retries safe.
type InstallationApproval struct {
	intent           string
	TargetID         string
	Generation       uint64
	ExpectedRevision uint64
	OperationID      string
}

// bindIntent captures caller input before registry defaults can change it.
func (a *InstallationApproval) bindIntent(request any) (*InstallationApproval, error) {
	if a == nil || a.intent != "" {
		return a, nil
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	copy := *a
	copy.intent = fmt.Sprintf("%x", sha256.Sum256(raw))
	return &copy, nil
}

// Intent identifies the original request, not its mutable server projections.
func (a *InstallationApproval) Intent() string { return a.intent }

func (a *InstallationApproval) Validate() error {
	if a == nil || a.Generation == 0 || uuid.Validate(a.TargetID) != nil || uuid.Validate(a.OperationID) != nil {
		return InstallationApprovalRefusal("INSTALLATION_APPROVAL_REQUIRED", "Install requires approval.target_id and generation from ListAvailableSolutions, expected_revision from the installation listing (zero if absent), and a fresh UUID operation_id; preserve the approval on retry")
	}
	return nil
}

// InstallationApprovalRefusal preserves a machine-readable reason on both RPCs.
func InstallationApprovalRefusal(reason, message string) error {
	st, err := status.New(codes.FailedPrecondition, reason+": "+message).WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "accounts.saas.codefly.dev"})
	if err != nil {
		return status.Error(codes.Internal, "installation approval refusal encoding failed")
	}
	return st.Err()
}

// InstallFixtureTarget is host-owned setup consent. Only fixture setup may
// resolve a fresh approval itself; human requests must carry their reviewed one.
func (s *Service) InstallFixtureTarget(ctx context.Context, actorID, orgID, target, role, ownerID string) (*gen.Installation, bool, error) {
	available, err := s.availableTargetNamed(ctx, orgID, target)
	if err != nil {
		return nil, false, err
	}
	if available.Installed && available.Kind != "module" {
		existing, err := s.heldTargetInstallation(ctx, orgID, available.TargetID)
		return existing, false, err
	}
	// The catalogue counts a solution as installed only through its canonical
	// record. A host without the authority core still keeps legacy solution
	// records, so the fixture finds its own here: installing it again would
	// resolve the role afresh and be refused as a changed immutable attribute
	// once the solution has delivered roles of its own.
	if _, err := s.AuthorityCore(); available.Kind != "module" && errors.Is(err, ErrAuthorityCoreUnconfigured) {
		existing, err := s.findHeldTargetInstallation(ctx, orgID, available.TargetID)
		if err != nil || existing != nil {
			return existing, false, err
		}
	}
	approval := &InstallationApproval{TargetID: available.TargetID, Generation: available.AppliedGeneration, OperationID: uuid.NewString()}
	for token := ""; ; {
		rows, next, err := s.ListOrganizationInstallations(ctx, orgID, token, listInstallationsMaxPageSize)
		if err != nil {
			return nil, false, err
		}
		for _, row := range rows {
			i := row.Summary.GetInstallation()
			if i.GetTargetId() == available.TargetID {
				approval.ExpectedRevision = max(approval.ExpectedRevision, i.GetRevision())
			}
		}
		if next == "" {
			break
		}
		token = next
	}
	return s.InstallTargetByName(ctx, actorID, orgID, target, role, ownerID, approval)
}
