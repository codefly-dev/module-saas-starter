package business

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"backend/pkg/gen"
)

const invitationTTL = 7 * 24 * time.Hour

// Invitation is the domain representation of an org invitation.
type Invitation struct {
	ID         string
	OrgID      string
	InviterID  string
	Email      string
	Role       string
	TokenHash  string
	Status     string // pending, accepted, revoked, expired
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	AcceptedBy string
	CreatedAt  time.Time
}

// CreateInvitation generates and stores an invitation token.
func (s *Service) CreateInvitation(ctx context.Context, inviterID string, req *gen.CreateInvitationRequest) (*gen.CreateInvitationResponse, error) {
	w := wool.Get(ctx).In("CreateInvitation")

	// Check seat quota
	if s.entitlements != nil {
		ok, err := s.entitlements.CheckQuota(ctx, req.OrgId, "seats")
		if err != nil {
			return nil, w.Wrapf(err, "cannot check seat quota")
		}
		if !ok {
			return nil, w.NewError("seat limit reached for your plan")
		}
	}

	// Generate token
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, w.Wrapf(err, "cannot generate token")
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(plaintext))
	tokenHash := hex.EncodeToString(h[:])

	role := req.Role
	if role == "" {
		role = "member"
	}

	inv := &Invitation{
		ID:        uuid.New().String(),
		OrgID:     req.OrgId,
		InviterID: inviterID,
		Email:     req.Email,
		Role:      role,
		TokenHash: tokenHash,
		Status:    "pending",
		ExpiresAt: time.Now().Add(invitationTTL),
	}

	if err := s.store.CreateInvitation(ctx, inv); err != nil {
		return nil, w.Wrapf(err, "cannot create invitation")
	}

	s.emit(ctx, inviterID, "user", "invitation.created", "invitation", inv.ID, req.OrgId)

	return &gen.CreateInvitationResponse{
		Invitation:  invitationToProto(inv),
		InviteToken: plaintext,
	}, nil
}

// AcceptInvitation accepts an invitation by token, adding the user to the org.
func (s *Service) AcceptInvitation(ctx context.Context, userID string, req *gen.AcceptInvitationRequest) (*gen.AcceptInvitationResponse, error) {
	w := wool.Get(ctx).In("AcceptInvitation")

	h := sha256.Sum256([]byte(req.Token))
	tokenHash := hex.EncodeToString(h[:])

	inv, err := s.store.GetInvitationByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, w.Wrapf(err, "cannot look up invitation")
	}
	if inv == nil {
		return nil, w.NewError("invalid invitation token")
	}
	if inv.Status != "pending" {
		return nil, w.NewError("invitation is no longer pending")
	}
	if time.Now().After(inv.ExpiresAt) {
		_ = s.store.UpdateInvitationStatus(ctx, inv.ID, "expired", "")
		return nil, w.NewError("invitation has expired")
	}

	// Add user to org
	if err := s.store.AddOrgMember(ctx, inv.OrgID, userID, inv.Role); err != nil {
		return nil, w.Wrapf(err, "cannot add member to org")
	}

	// Mark accepted
	if err := s.store.UpdateInvitationStatus(ctx, inv.ID, "accepted", userID); err != nil {
		return nil, w.Wrapf(err, "cannot update invitation status")
	}

	org, err := s.store.GetOrganization(ctx, inv.OrgID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot get organization")
	}

	s.emit(ctx, userID, "user", "invitation.accepted", "invitation", inv.ID, inv.OrgID)

	return &gen.AcceptInvitationResponse{Organization: org}, nil
}

// ListInvitations returns invitations for an org, optionally filtered by status.
func (s *Service) ListInvitations(ctx context.Context, req *gen.ListInvitationsRequest) (*gen.ListInvitationsResponse, error) {
	status := ""
	if req.Status != gen.InvitationStatus_INVITATION_STATUS_UNSPECIFIED {
		status = invitationStatusToString(req.Status)
	}

	invs, err := s.store.ListInvitations(ctx, req.OrgId, status)
	if err != nil {
		return nil, err
	}

	var protos []*gen.Invitation
	for _, inv := range invs {
		protos = append(protos, invitationToProto(inv))
	}
	return &gen.ListInvitationsResponse{Invitations: protos}, nil
}

// RevokeInvitation marks an invitation as revoked.
func (s *Service) RevokeInvitation(ctx context.Context, inviterID string, req *gen.RevokeInvitationRequest) error {
	s.emit(ctx, inviterID, "user", "invitation.revoked", "invitation", req.Id, "")
	return s.store.UpdateInvitationStatus(ctx, req.Id, "revoked", "")
}

func invitationToProto(inv *Invitation) *gen.Invitation {
	p := &gen.Invitation{
		Id:        inv.ID,
		OrgId:     inv.OrgID,
		InviterId: inv.InviterID,
		Email:     inv.Email,
		Role:      inv.Role,
		Status:    invitationStatusFromString(inv.Status),
	}
	if !inv.ExpiresAt.IsZero() {
		p.ExpiresAt = timestamppb.New(inv.ExpiresAt)
	}
	if !inv.CreatedAt.IsZero() {
		p.CreatedAt = timestamppb.New(inv.CreatedAt)
	}
	return p
}

func invitationStatusFromString(s string) gen.InvitationStatus {
	switch s {
	case "pending":
		return gen.InvitationStatus_INVITATION_STATUS_PENDING
	case "accepted":
		return gen.InvitationStatus_INVITATION_STATUS_ACCEPTED
	case "revoked":
		return gen.InvitationStatus_INVITATION_STATUS_REVOKED
	case "expired":
		return gen.InvitationStatus_INVITATION_STATUS_EXPIRED
	default:
		return gen.InvitationStatus_INVITATION_STATUS_UNSPECIFIED
	}
}

func invitationStatusToString(s gen.InvitationStatus) string {
	switch s {
	case gen.InvitationStatus_INVITATION_STATUS_PENDING:
		return "pending"
	case gen.InvitationStatus_INVITATION_STATUS_ACCEPTED:
		return "accepted"
	case gen.InvitationStatus_INVITATION_STATUS_REVOKED:
		return "revoked"
	case gen.InvitationStatus_INVITATION_STATUS_EXPIRED:
		return "expired"
	default:
		return ""
	}
}

