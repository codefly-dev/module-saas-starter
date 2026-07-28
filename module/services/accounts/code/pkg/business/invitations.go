package business

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/email"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const invitationTTL = 7 * 24 * time.Hour

const invitationEmailSource = "saas.accounts.invitations"

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

	appBase := s.appBaseURL
	if appBase == "" {
		appBase = "http://localhost:21931"
	}
	acceptPath := fmt.Sprintf("/invitations/accept?token=%s", plaintext)
	acceptURL := appBase + acceptPath

	var invitee *gen.User
	err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var err error
		invitee, err = s.store.GetUserByEmail(ctx, req.Email)
		return err
	})
	if err != nil {
		var storeErr *StoreError
		if !errors.As(err, &storeErr) || storeErr.StoreErrorType != ErrTypeNotFound {
			return nil, w.Wrapf(err, "cannot resolve invitation recipient")
		}
		invitee = nil
	}

	// The pending invitation reserves one seat. Quota inspection, stale
	// reservation cleanup, insertion, and the organization read all share the
	// same transaction and per-org quota lock. Existing recipients also supply a
	// user scope so preference evaluation and the in-app row commit atomically.
	var orgName string
	identity := Identity{OrgID: req.OrgId}
	if invitee != nil {
		identity.UserID = invitee.Uuid
	}
	if err := s.store.As(identity).Within(ctx, func(ctx context.Context) error {
		quota, err := s.cardinalityQuotaInTx(ctx, req.OrgId, EntitlementSeats)
		if err != nil {
			return w.Wrapf(err, "cannot check seat quota")
		}
		if err := quota.RequireAvailable(); err != nil {
			return err
		}
		if err := s.store.ExpirePendingInvitations(ctx, req.OrgId); err != nil {
			return w.Wrapf(err, "cannot expire stale invitations")
		}
		if err := s.store.CreateInvitation(ctx, inv); err != nil {
			return err
		}
		if org, err := s.store.GetOrganization(ctx, req.OrgId); err == nil && org != nil {
			orgName = org.Name
		}

		if orgName == "" {
			orgName = req.OrgId
		}
		sendInvitationEmail := true
		if invitee != nil {
			settings, err := s.store.GetUserSettings(ctx, invitee.Uuid)
			if err != nil {
				return w.Wrapf(err, "cannot read invitation preferences")
			}
			emailDecision, err := EvaluateNotificationDelivery(
				settings,
				NotificationCategoryProduct,
				NotificationChannelEmail,
			)
			if err != nil {
				return err
			}
			sendInvitationEmail = emailDecision.Deliver
			if _, err := s.createNotificationWithSettings(ctx, CreateNotificationInput{
				UserID:    invitee.Uuid,
				OrgID:     req.OrgId,
				Title:     "You've been invited",
				Body:      fmt.Sprintf("You've been invited to %s", orgName),
				Type:      "info",
				ActionURL: acceptPath,
				Category:  NotificationCategoryProduct,
			}, settings); err != nil {
				return w.Wrapf(err, "cannot create invitation notification")
			}
		}

		if s.emailOutbox != nil && sendInvitationEmail {
			if err := s.emailOutbox.EnqueueTemplate(ctx, email.TemplateRequest{
				DeliveryKey: inv.ID,
				Scope:       email.TenantScope(req.OrgId),
				Source:      invitationEmailSource,
				Template:    "invitation",
				To:          inv.Email,
				Variables: map[string]string{
					"invite_url":   acceptURL,
					"accept_url":   acceptURL,
					"org_name":     orgName,
					"inviter_name": inviterID,
					"role":         inv.Role,
					"email":        inv.Email,
				},
				Fallback: &email.Message{
					To:       []string{inv.Email},
					Subject:  "You're invited",
					HTMLBody: renderInviteHTML(acceptURL, inv.Role),
					TextBody: renderInviteText(acceptURL, inv.Role),
					Tags:     map[string]string{"type": "invitation", "org_id": inv.OrgID},
				},
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create invitation")
	}

	s.emit(ctx, inviterID, "user", "invitation.created", "invitation", inv.ID, req.OrgId)

	return &gen.CreateInvitationResponse{
		Invitation:  invitationToProto(inv),
		InviteToken: plaintext,
	}, nil
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
//
// Security: the caller MUST be the invited email holder. A leaked/shared
// token cannot be used by a different account — we look up the caller's
// user record and match the primary_email against inv.Email (case-
// insensitive). This closes the "token replay from another account" hole
// flagged as CRITICAL in the audit.
func (s *Service) AcceptInvitation(ctx context.Context, userID string, req *gen.AcceptInvitationRequest) (*gen.AcceptInvitationResponse, error) {
	w := wool.Get(ctx).In("AcceptInvitation")

	h := sha256.Sum256([]byte(req.Token))
	tokenHash := hex.EncodeToString(h[:])

	// Token-hash lookup is cross-tenant: the token doesn't carry the
	// org, the invitation row does. invitations is RLS-protected
	// (Phase 2B) so the read needs WithControlPlane; the result tells us
	// which org to enter for the rest of the flow.
	var inv *Invitation
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		i, err := s.store.GetInvitationByTokenHash(ctx, tokenHash)
		inv = i
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot look up invitation")
	}
	if inv == nil {
		return nil, w.NewError("invalid invitation token")
	}
	if inv.Status != "pending" {
		return nil, w.NewError("invitation is no longer pending")
	}
	if time.Now().After(inv.ExpiresAt) {
		_ = s.store.WithOrgTx(ctx, inv.OrgID, func(ctx context.Context) error {
			return s.store.UpdateInvitationStatus(ctx, inv.ID, "expired", "")
		})
		return nil, w.NewError("invitation has expired")
	}

	// Verify the caller is the invited email holder under their own user scope.
	caller, err := s.getUserAsSelf(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot resolve caller")
	}
	if caller == nil || !strings.EqualFold(caller.PrimaryEmail, inv.Email) {
		return nil, w.NewError("invitation was addressed to a different email")
	}

	// Acceptance is transactional: a second concurrent call must not be
	// able to observe a still-"pending" invitation after we've begun
	// accepting it. We re-check status INSIDE the transaction so the
	// check-and-act race (flagged as HIGH in the audit) is closed.
	// WithOrgTx replaces RunInTransaction so org_members + invitations
	// + organizations all see app.current_org_id and let the writes
	// through under their RLS policies.
	var org *gen.Organization
	if txErr := s.store.WithOrgTx(ctx, inv.OrgID, func(txCtx context.Context) error {
		fresh, err := s.store.GetInvitationByTokenHash(txCtx, tokenHash)
		if err != nil {
			return w.Wrapf(err, "cannot re-read invitation")
		}
		if fresh == nil || fresh.Status != "pending" {
			return w.NewError("invitation is no longer pending")
		}
		if err := s.store.AddOrgMember(txCtx, inv.OrgID, userID, inv.Role); err != nil {
			return w.Wrapf(err, "cannot add member to org")
		}
		if err := s.store.UpdateInvitationStatus(txCtx, inv.ID, "accepted", userID); err != nil {
			return w.Wrapf(err, "cannot update invitation status")
		}
		o, err := s.store.GetOrganization(txCtx, inv.OrgID)
		if err != nil {
			return w.Wrapf(err, "cannot get organization")
		}
		org = o
		return nil
	}); txErr != nil {
		return nil, txErr
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

	var invs []*Invitation
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		is, err := s.store.ListInvitations(ctx, req.OrgId, status)
		invs = is
		return err
	}); err != nil {
		return nil, err
	}

	var protos []*gen.Invitation
	for _, inv := range invs {
		protos = append(protos, invitationToProto(inv))
	}
	return &gen.ListInvitationsResponse{Invitations: protos}, nil
}

// RevokeInvitation marks an invitation as revoked. The proto only carries the
// invitation id, so the handler first resolves its organization under a narrow
// bypass and requires that tenant's admin role. This update then uses bypass
// because no organization id reaches the storage mutation itself.
func (s *Service) RevokeInvitation(ctx context.Context, inviterID string, req *gen.RevokeInvitationRequest) error {
	w := wool.Get(ctx).In("RevokeInvitation")
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.store.UpdateInvitationStatus(ctx, req.Id, "revoked", "")
	}); err != nil {
		return w.Wrapf(err, "cannot revoke invitation")
	}
	s.emit(ctx, inviterID, "user", "invitation.revoked", "invitation", req.Id, "")
	return nil
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
