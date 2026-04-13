package business

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/protobuf/types/known/timestamppb"

	"api/pkg/email"
	"api/pkg/gen"
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
		ID:        NewIDString(),
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

	// Best-effort email delivery. Failures are logged but do NOT fail
	// the invite creation — the inviter can always resend or copy the
	// link manually.
	if s.mail != nil {
		if _, err := s.sendInvitationEmail(ctx, inv, plaintext); err != nil {
			w.Debug("invitation email send failed", wool.ErrField(err))
		}
	}

	return &gen.CreateInvitationResponse{
		Invitation:  invitationToProto(inv),
		InviteToken: plaintext,
	}, nil
}

// sendInvitationEmail renders and delivers the invite message.
// The accept link includes the plaintext token as a query param;
// the frontend's /auth/accept page extracts it and calls AcceptInvitation.
//
// Prefers the "invitation" template from the template store when the
// TemplateSender is wired; falls back to the hardcoded HTML/text render
// when templates are unavailable.
func (s *Service) sendInvitationEmail(ctx context.Context, inv *Invitation, plainToken string) (string, error) {
	appBase := s.appBaseURL
	if appBase == "" {
		appBase = "http://localhost:21931"
	}

	acceptURL := fmt.Sprintf("%s/invitations/accept?token=%s", appBase, plainToken)

	// Prefer the template system when available.
	if s.templateMail != nil {
		return s.templateMail.SendTemplate(ctx, "invitation", inv.Email, map[string]string{
			"accept_url": acceptURL,
			"role":       inv.Role,
			"email":      inv.Email,
		})
	}

	// Fallback: raw Send with inline HTML.
	from := s.fromAddress
	if from == "" {
		from = "no-reply@localhost"
	}
	msg := &email.Message{
		From:     from,
		To:       []string{inv.Email},
		Subject:  "You're invited",
		HTMLBody: renderInviteHTML(acceptURL, inv.Role),
		TextBody: renderInviteText(acceptURL, inv.Role),
		Tags:     map[string]string{"type": "invitation", "org_id": inv.OrgID},
	}
	return s.mail.Send(ctx, msg)
}

func renderInviteHTML(acceptURL, role string) string {
	return fmt.Sprintf(`<!doctype html>
<html>
<body style="font-family: system-ui, sans-serif; max-width: 560px; margin: 40px auto; padding: 24px;">
<h2>You're invited</h2>
<p>You've been invited to join as <strong>%s</strong>.</p>
<p>
  <a href="%s" style="display:inline-block; padding:12px 24px; background:#0066cc; color:white; text-decoration:none; border-radius:6px;">
    Accept invitation
  </a>
</p>
<p style="color:#666; font-size:14px;">This link expires in 7 days. If you didn't expect this invitation, you can safely ignore this email.</p>
</body>
</html>`, role, acceptURL)
}

func renderInviteText(acceptURL, role string) string {
	return fmt.Sprintf(`You're invited to join as %s.

Accept the invitation: %s

This link expires in 7 days. If you didn't expect this invitation, you can safely ignore this email.
`, role, acceptURL)
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

