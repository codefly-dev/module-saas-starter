package business

// The approvals behind the artifact interface (codefly.dev/artifact). Who may
// approve, run or revoke is not decided here: the shared authority core decides
// it once for every host (authority.Service.DecideArtifact), and these are the
// records that decision is written to and read from. So this file keeps no
// rule of its own — no policy table, no second permission check, no owner
// tier — and every refusal a caller sees about authority comes from the core.
//
// The rows are the same `executable_artifact_approvals` the SaaS-only
// ModuleCapabilitiesService path writes, keyed the same way (organization,
// installation, module principal, subject digest), so an approval is one
// record however it was given.

import (
	"bytes"
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// HostArtifactApproval is one approval as the interface answers it: the record
// and the revision the artifact was approved at, which the identity carries
// and a revoke has to read back.
type HostArtifactApproval struct {
	ID       string
	Digest   string
	Revision int64
}

// ApproveHostArtifact records consent to the artifact the digest names, for an
// installation already decided by the core. It is idempotent by exact digest:
// approving the same artifact twice answers the first approval, and a revoked
// row is never resurrected.
func (s *Service) ApproveHostArtifact(ctx context.Context, tenant, owner, installation, module, policy, digest string, subject []byte, revision int64) (*HostArtifactApproval, error) {
	store, err := s.hostArtifactStore()
	if err != nil {
		return nil, err
	}
	ctx, release, err := s.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out *HostArtifactApproval
	err = s.store.As(Identity{OrgID: tenant, UserID: owner}).Within(ctx, func(ctx context.Context) error {
		row, inserted, err := store.PutExecutableArtifactApproval(ctx, &ExecutableArtifactApproval{
			ID: uuid.NewString(), OrgID: tenant, Installation: installation, Module: module,
			Policy: policy, Digest: digest, Subject: subject, ApprovedBy: owner,
		})
		if err != nil {
			return err
		}
		if row == nil || !bytes.Equal(row.Subject, subject) || row.RevokedAt != nil {
			return hostArtifactAbsent()
		}
		out = &HostArtifactApproval{ID: row.ID, Digest: row.Digest, Revision: revision}
		if !inserted {
			return nil
		}
		// Consent is the tenant's, attributed to the person who gave it; the
		// approver's later departure withdraws nothing.
		return s.emitTx(ctx, owner, ActorTypeUser, EventExecutableArtifactDecision, "executable_artifact", row.ID, tenant,
			map[string]any{"action": "approve", "installation_id": installation, "policy_id": policy, "subject_digest": digest, "module_principal_id": module})
	})
	return out, err
}

// AuthorizedHostArtifact is the standing approval of exactly this artifact, for
// a run. An artifact nobody approved, one approved at another revision (the
// revision is part of the digest) and a revoked one all read the same: no
// standing consent.
func (s *Service) AuthorizedHostArtifact(ctx context.Context, tenant, owner, installation, module, digest string, subject []byte, revision int64) (*HostArtifactApproval, error) {
	store, err := s.hostArtifactStore()
	if err != nil {
		return nil, err
	}
	ctx, release, err := s.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out *HostArtifactApproval
	err = s.store.As(Identity{OrgID: tenant, UserID: owner}).Within(ctx, func(ctx context.Context) error {
		row, err := store.GetExecutableArtifactApproval(ctx, tenant, installation, module, digest)
		if err != nil {
			return err
		}
		if row == nil || !bytes.Equal(row.Subject, subject) || row.RevokedAt != nil {
			return hostArtifactAbsent()
		}
		out = &HostArtifactApproval{ID: row.ID, Digest: row.Digest, Revision: revision}
		return nil
	})
	return out, err
}

// RevokeHostArtifact withdraws one approval by its id, for an installation the
// core already decided. It answers the approval it ended, at the revision it
// was approved at — read back from the retained record, since a revoke names
// no artifact. Revoking twice refuses: there is nothing standing to end.
func (s *Service) RevokeHostArtifact(ctx context.Context, tenant, owner, installation, module, id string, revisionOf func([]byte) int64) (*HostArtifactApproval, error) {
	store, err := s.hostArtifactStore()
	if err != nil {
		return nil, err
	}
	ctx, release, err := s.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out *HostArtifactApproval
	err = s.store.As(Identity{OrgID: tenant, UserID: owner}).Within(ctx, func(ctx context.Context) error {
		row, err := store.GetExecutableArtifactApprovalByID(ctx, tenant, installation, module, id)
		if err != nil {
			return err
		}
		if row == nil || row.RevokedAt != nil {
			return hostArtifactAbsent()
		}
		ended, err := store.RevokeExecutableArtifactApproval(ctx, tenant, installation, module, row.Digest, owner)
		if err != nil {
			return err
		}
		if !ended {
			return hostArtifactAbsent()
		}
		out = &HostArtifactApproval{ID: row.ID, Digest: row.Digest, Revision: revisionOf(row.Subject)}
		return s.emitTx(ctx, owner, ActorTypeUser, EventExecutableArtifactDecision, "executable_artifact", row.ID, tenant,
			map[string]any{"action": "revoke", "installation_id": installation, "policy_id": row.Policy, "subject_digest": row.Digest, "module_principal_id": module})
	})
	return out, err
}

func (s *Service) hostArtifactStore() (ExecutableArtifactStore, error) {
	store, ok := s.store.(ExecutableArtifactStore)
	if !ok {
		return nil, status.Error(codes.Unavailable, "artifact approval store unavailable")
	}
	return store, nil
}

// hostArtifactAbsent is the one answer for "no standing approval of exactly
// this artifact": an unapproved artifact, one approved at another revision and
// a revoked one are not told apart, so a caller learns nothing by asking.
func hostArtifactAbsent() error {
	return status.Error(codes.PermissionDenied, "no standing approval of exactly this artifact")
}
