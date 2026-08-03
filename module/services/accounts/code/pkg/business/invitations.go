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

const (
	invitationTTL            = 7 * 24 * time.Hour
	invitationResendCooldown = time.Minute
	invitationEmailSource    = "saas.accounts.invitations"
)

var (
	ErrInvitationUnavailable     = errors.New("invitation unavailable")
	ErrInvitationEmailMismatch   = errors.New("sign in with the email address that received this invitation")
	ErrInvitationEmailUnverified = errors.New("verify your email address before accepting this invitation")
	ErrInvitationExpired         = errors.New("invitation expired; ask the inviter to send a new one")
)

type Invitation struct {
	ID                 string
	OrgID              string
	InviterID          string
	InviterDisplayName string
	Email              string
	Role               string
	TokenHash          string
	Status             string
	DeliveryStatus     string
	ExpiresAt          time.Time
	AcceptedAt         *time.Time
	AcceptedBy         string
	CreatedAt          time.Time
	LastSentAt         *time.Time
	SendCount          uint32
	LastResendKeyHash  string
}

func (s *Service) CreateInvitation(
	ctx context.Context,
	inviterID string,
	req *gen.CreateInvitationRequest,
) (*gen.CreateInvitationResponse, error) {
	w := wool.Get(ctx).In("CreateInvitation")

	role, ok := invitationRoleToString(req.Role)
	if !ok {
		return nil, w.NewError("invitation role must be member or admin")
	}
	normalizedEmail := strings.ToLower(strings.TrimSpace(req.Email))

	inviter, err := s.getUserAsSelf(ctx, inviterID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot resolve inviter")
	}
	inviterName := normalizedEmail
	if inviter != nil {
		inviterName = inviter.PrimaryEmail
		if name := strings.TrimSpace(inviter.Profile["name"]); name != "" {
			inviterName = name
		}
	}

	plaintext, tokenHash, err := newInvitationToken()
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate invitation credential")
	}
	now := time.Now().UTC()
	inv := &Invitation{
		ID:                 NewIDString(),
		OrgID:              req.OrgId,
		InviterID:          inviterID,
		InviterDisplayName: inviterName,
		Email:              normalizedEmail,
		Role:               role,
		TokenHash:          tokenHash,
		Status:             "pending",
		DeliveryStatus:     "disabled",
		ExpiresAt:          now.Add(invitationTTL),
		CreatedAt:          now,
	}

	var invitee *gen.User
	err = s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var lookupErr error
		invitee, lookupErr = s.store.GetUserByEmail(ctx, normalizedEmail)
		return lookupErr
	})
	if err != nil {
		var storeErr *StoreError
		if !errors.As(err, &storeErr) || storeErr.StoreErrorType != ErrTypeNotFound {
			return nil, w.Wrapf(err, "cannot resolve invitation recipient")
		}
		invitee = nil
	}

	var orgName string
	identity := Identity{OrgID: req.OrgId}
	if invitee != nil {
		identity.UserID = invitee.Uuid
	}
	if err := s.store.As(identity).Within(ctx, func(ctx context.Context) error {
		if err := s.store.ExpirePendingInvitations(ctx, req.OrgId); err != nil {
			return w.Wrapf(err, "cannot expire stale invitations")
		}
		quota, err := s.cardinalityQuotaInTx(ctx, req.OrgId, EntitlementSeats)
		if err != nil {
			return w.Wrapf(err, "cannot check seat quota")
		}
		if err := quota.RequireAvailable(); err != nil {
			return err
		}
		if org, err := s.store.GetOrganization(ctx, req.OrgId); err == nil && org != nil {
			orgName = org.Name
		}

		sendInvitationEmail := s.emailOutbox != nil
		var settings *gen.UserSettings
		if invitee != nil {
			var err error
			settings, err = s.store.GetUserSettings(ctx, invitee.Uuid)
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
			sendInvitationEmail = sendInvitationEmail && emailDecision.Deliver
		}
		if sendInvitationEmail {
			inv.DeliveryStatus = "queued"
			inv.LastSentAt = &now
			inv.SendCount = 1
		}
		if err := s.store.CreateInvitation(ctx, inv); err != nil {
			return err
		}
		if invitee != nil {
			if _, err := s.createNotificationWithSettings(ctx, CreateNotificationInput{
				UserID:         invitee.Uuid,
				OrgID:          req.OrgId,
				Title:          "You've been invited",
				Body:           fmt.Sprintf("%s invited you to join %s", inviterName, nonEmpty(orgName, "an organization")),
				Type:           "info",
				ActionURL:      "/invitations/accept?id=" + inv.ID,
				Category:       NotificationCategoryProduct,
				IdempotencyKey: "invitation:" + inv.ID,
			}, settings); err != nil {
				return w.Wrapf(err, "cannot create invitation notification")
			}
		}
		if sendInvitationEmail {
			if err := s.enqueueInvitationEmail(ctx, inv, plaintext, orgName, inv.ID); err != nil {
				return err
			}
		}
		return s.captureProductEvent(
			ctx,
			"invite_created",
			inv.ID,
			inviterID,
			req.OrgId,
			inv.CreatedAt,
			map[string]any{"role": inv.Role},
		)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create invitation")
	}

	s.emit(ctx, inviterID, "user", "invitation.created", "invitation", inv.ID, req.OrgId)

	return &gen.CreateInvitationResponse{Invitation: invitationToProto(inv)}, nil
}

func (s *Service) InspectInvitation(
	ctx context.Context,
	req *gen.InspectInvitationRequest,
) (*gen.InvitationSummary, error) {
	inv, err := s.invitationByToken(ctx, req.Token)
	if err != nil {
		return nil, wool.Get(ctx).Wrapf(err, "cannot inspect invitation")
	}
	return s.summarizeInvitation(ctx, inv)
}

func (s *Service) InspectInvitationByID(
	ctx context.Context,
	userID string,
	req *gen.InspectInvitationByIdRequest,
) (*gen.InvitationSummary, error) {
	var inv *Invitation
	err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var lookupErr error
		inv, lookupErr = s.store.GetInvitationByID(ctx, req.InvitationId)
		return lookupErr
	})
	if err != nil {
		return nil, wool.Get(ctx).Wrapf(err, "cannot inspect invitation")
	}
	if inv == nil {
		return nil, ErrInvitationUnavailable
	}
	caller, err := s.getUserAsSelf(ctx, userID)
	if err != nil {
		return nil, wool.Get(ctx).Wrapf(err, "cannot resolve caller")
	}
	if caller == nil || !strings.EqualFold(caller.PrimaryEmail, inv.Email) {
		return nil, ErrInvitationEmailMismatch
	}
	return s.summarizeInvitation(ctx, inv)
}

func (s *Service) summarizeInvitation(
	ctx context.Context,
	inv *Invitation,
) (*gen.InvitationSummary, error) {
	if inv == nil {
		return nil, ErrInvitationUnavailable
	}
	if inv.Status == "pending" && time.Now().After(inv.ExpiresAt) {
		if err := s.store.WithOrgTx(ctx, inv.OrgID, func(ctx context.Context) error {
			transitioned, err := s.store.UpdateInvitationStatus(ctx, inv.ID, "expired", "")
			if err != nil {
				return err
			}
			if transitioned {
				inv.Status = "expired"
				return s.captureProductEvent(
					ctx,
					"invite_expired",
					inv.ID,
					"",
					inv.OrgID,
					inv.ExpiresAt,
					nil,
				)
			}
			inv, err = s.store.GetInvitationByID(ctx, inv.ID)
			return err
		}); err != nil {
			return nil, wool.Get(ctx).Wrapf(err, "cannot resolve invitation status")
		}
		if inv == nil {
			return nil, ErrInvitationUnavailable
		}
	}
	return &gen.InvitationSummary{
		Status:    invitationStatusFromString(inv.Status),
		Role:      invitationRoleFromString(inv.Role),
		EmailHint: maskEmail(inv.Email),
		ExpiresAt: timestamppb.New(inv.ExpiresAt),
	}, nil
}

func (s *Service) AcceptInvitation(
	ctx context.Context,
	userID string,
	req *gen.AcceptInvitationRequest,
) (*gen.AcceptInvitationResponse, error) {
	w := wool.Get(ctx).In("AcceptInvitation")

	var inv *Invitation
	var err error
	switch credential := req.Credential.(type) {
	case *gen.AcceptInvitationRequest_Token:
		inv, err = s.invitationByToken(ctx, credential.Token)
	case *gen.AcceptInvitationRequest_InvitationId:
		err = s.store.As(System()).Within(ctx, func(ctx context.Context) error {
			var lookupErr error
			inv, lookupErr = s.store.GetInvitationByID(ctx, credential.InvitationId)
			return lookupErr
		})
	default:
		return nil, ErrInvitationUnavailable
	}
	if err != nil {
		return nil, w.Wrapf(err, "cannot inspect invitation")
	}
	if inv == nil {
		return nil, ErrInvitationUnavailable
	}

	caller, err := s.getUserAsSelf(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot resolve caller")
	}
	if caller == nil || !strings.EqualFold(caller.PrimaryEmail, inv.Email) {
		return nil, ErrInvitationEmailMismatch
	}
	// Acceptance authorizes on email equality, so it is only sound when the
	// caller's address is verified. An unverified account may sign in but
	// cannot join an organization addressed to an email it has not proven it
	// controls. Inspection stays open — it grants no membership.
	if !caller.EmailVerified {
		return nil, ErrInvitationEmailUnverified
	}
	if inv.Status == "accepted" && inv.AcceptedBy == userID {
		org, getErr := s.organizationForInvitation(ctx, inv)
		if getErr != nil {
			return nil, getErr
		}
		return &gen.AcceptInvitationResponse{Organization: org, AlreadyAccepted: true}, nil
	}
	if inv.Status != "pending" {
		return nil, ErrInvitationUnavailable
	}
	if time.Now().After(inv.ExpiresAt) {
		_ = s.store.WithOrgTx(ctx, inv.OrgID, func(ctx context.Context) error {
			_, err := s.store.UpdateInvitationStatus(ctx, inv.ID, "expired", "")
			return err
		})
		return nil, ErrInvitationExpired
	}

	var org *gen.Organization
	alreadyAccepted := false
	if txErr := s.store.As(Identity{UserID: userID, OrgID: inv.OrgID}).Within(ctx, func(ctx context.Context) error {
		fresh, lockErr := s.store.GetInvitationByID(ctx, inv.ID)
		if lockErr != nil {
			return lockErr
		}
		if fresh == nil {
			return ErrInvitationUnavailable
		}
		if fresh.Status == "accepted" && fresh.AcceptedBy == userID {
			alreadyAccepted = true
		} else {
			if fresh.Status != "pending" || time.Now().After(fresh.ExpiresAt) {
				return ErrInvitationUnavailable
			}
			if err := s.store.AddOrgMember(ctx, inv.OrgID, userID, inv.Role); err != nil {
				return w.Wrapf(err, "cannot add member to organization")
			}
			acceptedAt := time.Now().UTC()
			updated, err := s.store.UpdateInvitationStatus(ctx, inv.ID, "accepted", userID)
			if err != nil {
				return w.Wrapf(err, "cannot accept invitation")
			}
			if !updated {
				return ErrInvitationUnavailable
			}
			if err := s.captureProductEvent(
				ctx,
				"invite_accepted",
				inv.ID,
				userID,
				inv.OrgID,
				acceptedAt,
				map[string]any{"role": inv.Role},
			); err != nil {
				return err
			}
		}
		var getErr error
		org, getErr = s.store.GetOrganization(ctx, inv.OrgID)
		return getErr
	}); txErr != nil {
		return nil, txErr
	}

	s.invalidateMembership(ctx, inv.OrgID, userID)
	if !alreadyAccepted {
		s.emit(ctx, userID, "user", "invitation.accepted", "invitation", inv.ID, inv.OrgID)
		_, _ = s.CreateNotification(ctx, CreateNotificationInput{
			UserID:    inv.InviterID,
			OrgID:     inv.OrgID,
			Title:     "Invitation accepted",
			Body:      fmt.Sprintf("%s joined %s", inv.Email, org.Name),
			Type:      "success",
			ActionURL: "/admin/invitations",
			Category:  NotificationCategoryProduct,
		})
	}
	return &gen.AcceptInvitationResponse{Organization: org, AlreadyAccepted: alreadyAccepted}, nil
}

func (s *Service) ListInvitations(
	ctx context.Context,
	req *gen.ListInvitationsRequest,
) (*gen.ListInvitationsResponse, error) {
	status := ""
	if req.Status != gen.InvitationStatus_INVITATION_STATUS_UNSPECIFIED {
		status = invitationStatusToString(req.Status)
	}
	var invitations []*Invitation
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		var err error
		invitations, err = s.store.ListInvitations(ctx, req.OrgId, status)
		return err
	}); err != nil {
		return nil, err
	}
	protos := make([]*gen.Invitation, 0, len(invitations))
	for _, invitation := range invitations {
		protos = append(protos, invitationToProto(invitation))
	}
	return &gen.ListInvitationsResponse{Invitations: protos}, nil
}

func (s *Service) ResendInvitation(
	ctx context.Context,
	actorID string,
	req *gen.ResendInvitationRequest,
	idempotencyKey string,
) (*gen.Invitation, error) {
	w := wool.Get(ctx).In("ResendInvitation")
	if s.emailOutbox == nil {
		return nil, w.NewError("invitation delivery is unavailable")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 255 {
		return nil, w.NewError("valid Idempotency-Key header required")
	}
	keyHashBytes := sha256.Sum256([]byte(idempotencyKey))
	keyHash := hex.EncodeToString(keyHashBytes[:])
	var inv *Invitation
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var err error
		inv, err = s.store.GetInvitationByID(ctx, req.Id)
		return err
	}); err != nil || inv == nil {
		return nil, w.NewError("invitation not found")
	}
	if inv.LastResendKeyHash == keyHash {
		return invitationToProto(inv), nil
	}
	if inv.Status != "pending" {
		return nil, w.NewError("only pending invitations can be resent")
	}
	if inv.LastSentAt != nil && time.Since(*inv.LastSentAt) < invitationResendCooldown {
		return nil, w.NewError("invitation was sent recently; try again later")
	}

	plaintext, tokenHash, err := newInvitationToken()
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate invitation credential")
	}
	now := time.Now()
	var orgName string
	replayed := false
	if err := s.store.WithOrgTx(ctx, inv.OrgID, func(ctx context.Context) error {
		fresh, err := s.store.GetInvitationByID(ctx, inv.ID)
		if err != nil || fresh == nil {
			return w.NewError("invitation not found")
		}
		if fresh.Status != "pending" {
			return w.NewError("only pending invitations can be resent")
		}
		if fresh.LastResendKeyHash == keyHash {
			inv = fresh
			replayed = true
			return nil
		}
		if fresh.LastSentAt != nil && time.Since(*fresh.LastSentAt) < invitationResendCooldown {
			return w.NewError("invitation was sent recently; try again later")
		}
		fresh.TokenHash = tokenHash
		fresh.ExpiresAt = now.Add(invitationTTL)
		fresh.LastSentAt = &now
		fresh.SendCount++
		fresh.DeliveryStatus = "queued"
		fresh.LastResendKeyHash = keyHash
		if err := s.store.RotateInvitationToken(ctx, fresh); err != nil {
			return err
		}
		inv = fresh
		if org, getErr := s.store.GetOrganization(ctx, inv.OrgID); getErr == nil && org != nil {
			orgName = org.Name
		}
		return s.enqueueInvitationEmail(
			ctx, inv, plaintext, orgName, fmt.Sprintf("%s:%d", inv.ID, inv.SendCount),
		)
	}); err != nil {
		return nil, err
	}
	if !replayed {
		s.emit(ctx, actorID, "user", "invitation.resent", "invitation", inv.ID, inv.OrgID)
	}
	return invitationToProto(inv), nil
}

func (s *Service) RevokeInvitation(
	ctx context.Context,
	inviterID string,
	req *gen.RevokeInvitationRequest,
) error {
	var inv *Invitation
	revoked := false
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		inv, err = s.store.GetInvitationByID(ctx, req.Id)
		if err != nil {
			return err
		}
		if inv == nil {
			return ErrInvitationUnavailable
		}
		if inv.Status == "revoked" {
			return nil
		}
		if inv.Status != "pending" {
			return ErrInvitationUnavailable
		}
		return nil
	}); err != nil {
		return wool.Get(ctx).Wrapf(err, "cannot revoke invitation")
	}
	if inv.Status == "revoked" {
		return nil
	}
	revokedAt := time.Now().UTC()
	if err := s.store.WithOrgTx(ctx, inv.OrgID, func(ctx context.Context) error {
		var err error
		revoked, err = s.store.UpdateInvitationStatus(ctx, req.Id, "revoked", "")
		if err != nil || !revoked {
			return err
		}
		return s.captureProductEvent(
			ctx,
			"invite_revoked",
			req.Id,
			inviterID,
			inv.OrgID,
			revokedAt,
			nil,
		)
	}); err != nil {
		return wool.Get(ctx).Wrapf(err, "cannot revoke invitation")
	}
	if revoked {
		s.emit(ctx, inviterID, "user", "invitation.revoked", "invitation", req.Id, inv.OrgID)
	}
	return nil
}

func (s *Service) invitationByToken(ctx context.Context, token string) (*Invitation, error) {
	var inv *Invitation
	err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var lookupErr error
		inv, lookupErr = s.store.GetInvitationByTokenHash(ctx, HashInvitationToken(token))
		return lookupErr
	})
	return inv, err
}

// HashInvitationToken derives the stored token_hash for a plaintext invitation
// credential. It is the single canonical hashing used both when persisting an
// invitation and when redeeming one, including from the auth resolver's invite
// flow.
func HashInvitationToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// announceInvitationAccepted performs the non-transactional side effects that
// follow an invitation accepted through the authentication path: the analytics
// event, the inviter notification, the audit event, and membership-cache
// invalidation. The acceptance itself is committed transactionally by the
// identity resolver; this mirrors the tail of AcceptInvitation for the
// auth-time flow, where the resolver has no access to the event pipeline.
func (s *Service) announceInvitationAccepted(ctx context.Context, inv *Invitation, userID string) {
	acceptedAt := time.Now().UTC()
	_ = s.store.As(Identity{UserID: userID, OrgID: inv.OrgID}).Within(ctx, func(ctx context.Context) error {
		return s.captureProductEvent(
			ctx,
			"invite_accepted",
			inv.ID,
			userID,
			inv.OrgID,
			acceptedAt,
			map[string]any{"role": inv.Role},
		)
	})
	s.invalidateMembership(ctx, inv.OrgID, userID)
	s.emit(ctx, userID, "user", "invitation.accepted", "invitation", inv.ID, inv.OrgID)

	orgName := "the organization"
	if org, err := s.organizationForInvitation(ctx, inv); err == nil && org != nil && org.Name != "" {
		orgName = org.Name
	}
	_, _ = s.CreateNotification(ctx, CreateNotificationInput{
		UserID:    inv.InviterID,
		OrgID:     inv.OrgID,
		Title:     "Invitation accepted",
		Body:      fmt.Sprintf("%s joined %s", inv.Email, orgName),
		Type:      "success",
		ActionURL: "/admin/invitations",
		Category:  NotificationCategoryProduct,
	})
}

// expireInvitation transitions a time-expired invitation to the expired state
// in its own transaction. The identity resolver fails an expired invite closed
// inside its serializable transaction, which therefore rolls back and cannot
// persist the transition; the status change happens here at the business layer,
// matching AcceptInvitation's lazy expiry.
func (s *Service) expireInvitation(ctx context.Context, inv *Invitation) {
	_ = s.store.WithOrgTx(ctx, inv.OrgID, func(ctx context.Context) error {
		_, err := s.store.UpdateInvitationStatus(ctx, inv.ID, "expired", "")
		return err
	})
}

func (s *Service) organizationForInvitation(
	ctx context.Context,
	inv *Invitation,
) (*gen.Organization, error) {
	var org *gen.Organization
	err := s.store.WithOrgTx(ctx, inv.OrgID, func(ctx context.Context) error {
		var getErr error
		org, getErr = s.store.GetOrganization(ctx, inv.OrgID)
		return getErr
	})
	return org, err
}

func newInvitationToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(plaintext))
	return plaintext, hex.EncodeToString(hash[:]), nil
}

func (s *Service) enqueueInvitationEmail(
	ctx context.Context,
	inv *Invitation,
	plaintext, orgName, deliveryKey string,
) error {
	if s.emailOutbox == nil {
		return nil
	}
	appBase := s.publicBaseURL(ctx)
	if appBase == "" {
		return wool.Get(ctx).NewError("public application origin is unavailable")
	}
	acceptURL := fmt.Sprintf(
		"%s/invitations/accept?token=%s",
		appBase,
		plaintext,
	)
	return s.emailOutbox.EnqueueTemplate(ctx, email.TemplateRequest{
		DeliveryKey: deliveryKey,
		Scope:       email.TenantScope(inv.OrgID),
		Source:      invitationEmailSource,
		Template:    "invitation",
		To:          inv.Email,
		Variables: map[string]string{
			"invite_url":   acceptURL,
			"accept_url":   acceptURL,
			"org_name":     nonEmpty(orgName, "the organization"),
			"inviter_name": inv.InviterDisplayName,
			"role":         inv.Role,
			"email":        inv.Email,
		},
		Tags: map[string]string{
			"type":          "invitation",
			"org_id":        inv.OrgID,
			"invitation_id": inv.ID,
		},
		Fallback: &email.Message{
			To:       []string{inv.Email},
			Subject:  "You're invited",
			HTMLBody: renderInviteHTML(acceptURL, inv.Role),
			TextBody: renderInviteText(acceptURL, inv.Role),
			Tags:     map[string]string{"type": "invitation", "org_id": inv.OrgID, "invitation_id": inv.ID},
		},
	})
}

func renderInviteHTML(acceptURL, role string) string {
	return fmt.Sprintf(`<!doctype html>
<html><body style="font-family:system-ui,sans-serif;max-width:560px;margin:40px auto;padding:24px">
<h2>You're invited</h2><p>You've been invited to join as <strong>%s</strong>.</p>
<p><a href="%s" style="display:inline-block;padding:12px 24px;background:#06c;color:#fff;text-decoration:none;border-radius:6px">Accept invitation</a></p>
<p style="color:#666;font-size:14px">This link expires in 7 days. If you didn't expect it, you can safely ignore this email.</p>
</body></html>`, role, acceptURL)
}

func renderInviteText(acceptURL, role string) string {
	return fmt.Sprintf("You're invited to join as %s.\n\nAccept the invitation: %s\n\nThis link expires in 7 days.\n", role, acceptURL)
}

func invitationToProto(inv *Invitation) *gen.Invitation {
	out := &gen.Invitation{
		Id:                 inv.ID,
		OrgId:              inv.OrgID,
		InviterId:          inv.InviterID,
		InviterDisplayName: inv.InviterDisplayName,
		Email:              inv.Email,
		Role:               invitationRoleFromString(inv.Role),
		Status:             invitationStatusFromString(inv.Status),
		DeliveryStatus:     invitationDeliveryStatusFromString(inv.DeliveryStatus),
		SendCount:          inv.SendCount,
	}
	if !inv.ExpiresAt.IsZero() {
		out.ExpiresAt = timestamppb.New(inv.ExpiresAt)
	}
	if !inv.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(inv.CreatedAt)
	}
	if inv.LastSentAt != nil {
		out.LastSentAt = timestamppb.New(*inv.LastSentAt)
	}
	return out
}

func invitationRoleToString(role gen.InvitationRole) (string, bool) {
	switch role {
	case gen.InvitationRole_INVITATION_ROLE_MEMBER:
		return "member", true
	case gen.InvitationRole_INVITATION_ROLE_ADMIN:
		return "admin", true
	default:
		return "", false
	}
}

func invitationRoleFromString(role string) gen.InvitationRole {
	switch role {
	case "member":
		return gen.InvitationRole_INVITATION_ROLE_MEMBER
	case "admin":
		return gen.InvitationRole_INVITATION_ROLE_ADMIN
	default:
		return gen.InvitationRole_INVITATION_ROLE_UNSPECIFIED
	}
}

func invitationStatusFromString(status string) gen.InvitationStatus {
	switch status {
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

func invitationStatusToString(status gen.InvitationStatus) string {
	switch status {
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

func invitationDeliveryStatusFromString(status string) gen.InvitationDeliveryStatus {
	switch status {
	case "disabled":
		return gen.InvitationDeliveryStatus_INVITATION_DELIVERY_STATUS_DISABLED
	case "queued":
		return gen.InvitationDeliveryStatus_INVITATION_DELIVERY_STATUS_QUEUED
	case "sent":
		return gen.InvitationDeliveryStatus_INVITATION_DELIVERY_STATUS_SENT
	case "delivered":
		return gen.InvitationDeliveryStatus_INVITATION_DELIVERY_STATUS_DELIVERED
	case "bounced":
		return gen.InvitationDeliveryStatus_INVITATION_DELIVERY_STATUS_BOUNCED
	case "complained":
		return gen.InvitationDeliveryStatus_INVITATION_DELIVERY_STATUS_COMPLAINED
	default:
		return gen.InvitationDeliveryStatus_INVITATION_DELIVERY_STATUS_UNSPECIFIED
	}
}

func maskEmail(value string) string {
	parts := strings.Split(value, "@")
	if len(parts) != 2 || parts[0] == "" {
		return ""
	}
	return parts[0][:1] + "***@" + parts[1]
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
