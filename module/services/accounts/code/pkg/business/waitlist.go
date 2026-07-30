package business

import (
	"accounts/pkg/abuse"
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

	"accounts/pkg/auth"
	"accounts/pkg/email"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	waitlistVerificationTTL      = 24 * time.Hour
	waitlistVerificationCooldown = time.Minute
	waitlistEmailSource          = "saas.accounts.waitlist"
	CurrentConsentPolicyVersion  = "2026-07-28"
)

type WaitlistEntry struct {
	ID                      string
	Email                   string
	Name                    string
	Company                 string
	UseCase                 string
	State                   string
	VerificationTokenHash   string
	VerificationExpiresAt   *time.Time
	VerificationSentAt      *time.Time
	Source                  string
	Campaign                string
	Referrer                string
	ReferralCode            string
	ReferredBy              string
	MarketingConsent        bool
	ConsentPolicyVersion    string
	AdminNotes              string
	Tags                    []string
	CreatedAt               time.Time
	VerifiedAt              *time.Time
	ApprovedAt              *time.Time
	InvitedAt               *time.Time
	ConvertedAt             *time.Time
	UnsubscribedAt          *time.Time
	ConvertedUserID         string
	ConvertedOrganizationID string
}

type WaitlistUpsertResult struct {
	Entry                  *WaitlistEntry
	Created                bool
	ShouldSendVerification bool
}

type WaitlistVerificationResult struct {
	Entry        *WaitlistEntry
	Transitioned bool
}

func (s *Service) SetAcquisitionMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "open_signup":
		s.acquisitionMode = gen.AcquisitionMode_ACQUISITION_MODE_OPEN_SIGNUP
	case "invite_only":
		s.acquisitionMode = gen.AcquisitionMode_ACQUISITION_MODE_INVITE_ONLY
	case "approval_required":
		s.acquisitionMode = gen.AcquisitionMode_ACQUISITION_MODE_APPROVAL_REQUIRED
	case "closed":
		s.acquisitionMode = gen.AcquisitionMode_ACQUISITION_MODE_CLOSED
	default:
		return fmt.Errorf("unsupported ACQUISITION_MODE %q", mode)
	}
	return nil
}

func (s *Service) SetWaitlistEmailVerification(required bool) {
	s.waitlistEmailVerification = required
}

func (s *Service) AcquisitionStatus() *gen.AcquisitionStatus {
	return &gen.AcquisitionStatus{
		Mode: s.acquisitionMode,
		WaitlistEnabled: s.acquisitionMode == gen.AcquisitionMode_ACQUISITION_MODE_INVITE_ONLY ||
			s.acquisitionMode == gen.AcquisitionMode_ACQUISITION_MODE_APPROVAL_REQUIRED,
		EmailVerificationRequired: s.waitlistEmailVerification,
		ConsentPolicyVersion:      CurrentConsentPolicyVersion,
	}
}

func (s *Service) JoinWaitlist(
	ctx context.Context,
	req *gen.JoinWaitlistRequest,
) (*gen.JoinWaitlistResponse, error) {
	generic := &gen.JoinWaitlistResponse{
		Message: "If this address can join the waitlist, check its inbox for the next step.",
	}
	if s.waitlistEmailVerification && s.emailOutbox == nil {
		generic.Message = "Your request was recorded, but email verification is temporarily unavailable. Contact support for access."
	}
	if req.Website != "" || s.acquisitionMode == gen.AcquisitionMode_ACQUISITION_MODE_CLOSED {
		return generic, nil
	}
	if err := s.abuseVerifier.Verify(ctx, abuse.Challenge{
		Token: req.GetTurnstileToken(), Action: "waitlist_join",
	}); err != nil {
		return nil, err
	}
	if req.PolicyVersion != CurrentConsentPolicyVersion {
		return nil, wool.Get(ctx).NewError("consent policy version is no longer current")
	}

	plaintext, hash, err := newWaitlistToken()
	if err != nil {
		return nil, err
	}
	referralCode, err := newReferralCode()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	entry := &WaitlistEntry{
		ID:                    NewIDString(),
		Email:                 strings.ToLower(strings.TrimSpace(req.Email)),
		Name:                  strings.TrimSpace(req.Name),
		Company:               strings.TrimSpace(req.Company),
		UseCase:               strings.TrimSpace(req.UseCase),
		State:                 "pending",
		VerificationTokenHash: hash,
		Source:                strings.TrimSpace(req.Source),
		Campaign:              strings.TrimSpace(req.Campaign),
		Referrer:              strings.TrimSpace(req.Referrer),
		ReferralCode:          referralCode,
		MarketingConsent:      req.MarketingConsent,
		ConsentPolicyVersion:  req.PolicyVersion,
		CreatedAt:             now,
	}
	expiresAt := now.Add(waitlistVerificationTTL)
	entry.VerificationExpiresAt = &expiresAt
	if !s.waitlistEmailVerification {
		entry.State = "verified"
		entry.VerifiedAt = &now
	}

	var result *WaitlistUpsertResult
	err = s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		if req.ReferralCode != "" {
			var lookupErr error
			entry.ReferredBy, lookupErr = s.store.GetWaitlistReferralID(ctx, req.ReferralCode)
			if lookupErr != nil {
				return lookupErr
			}
		}
		var upsertErr error
		result, upsertErr = s.store.UpsertWaitlistEntry(
			ctx, entry, waitlistVerificationCooldown,
		)
		if upsertErr != nil {
			return upsertErr
		}
		entry = result.Entry
		if result.ShouldSendVerification && s.emailOutbox != nil {
			appBase := s.publicBaseURL(ctx)
			if appBase == "" {
				return wool.Get(ctx).NewError("public application origin is unavailable")
			}
			verifyURL := fmt.Sprintf(
				"%s/waitlist/verify?token=%s",
				appBase,
				plaintext,
			)
			return s.emailOutbox.EnqueueTemplate(ctx, email.TemplateRequest{
				DeliveryKey: entry.ID + ":" + hash[:12],
				Scope:       email.GlobalScope(),
				Source:      waitlistEmailSource,
				Template:    "waitlist_verification",
				To:          entry.Email,
				Variables:   map[string]string{"verify_url": verifyURL},
				Fallback: &email.Message{
					To:       []string{entry.Email},
					Subject:  "Confirm your waitlist request",
					HTMLBody: fmt.Sprintf(`<p>Confirm your waitlist request:</p><p><a href="%s">Verify email</a></p><p>This link expires in 24 hours.</p>`, verifyURL),
					TextBody: fmt.Sprintf("Confirm your waitlist request: %s\nThis link expires in 24 hours.\n", verifyURL),
					Tags:     map[string]string{"type": "waitlist_verification"},
				},
			})
		}
		return nil
	})
	if err != nil {
		return nil, wool.Get(ctx).Wrapf(err, "waitlist request could not be queued")
	}

	if result != nil && result.Created {
		s.emit(ctx, entry.ID, "waitlist", "waitlist.joined", "waitlist_entry", entry.ID, "")
	}
	return generic, nil
}

func (s *Service) VerifyWaitlist(
	ctx context.Context,
	req *gen.VerifyWaitlistRequest,
) (*gen.VerifyWaitlistResponse, error) {
	hash := sha256.Sum256([]byte(req.Token))
	var result *WaitlistVerificationResult
	err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var verifyErr error
		result, verifyErr = s.store.VerifyWaitlistEntry(ctx, hex.EncodeToString(hash[:]), time.Now())
		return verifyErr
	})
	if err != nil {
		if errors.Is(err, ErrWaitlistTokenExpired) {
			return &gen.VerifyWaitlistResponse{
				State:   gen.WaitlistState_WAITLIST_STATE_PENDING,
				Message: "This verification link expired. Submit the form again to receive a new one.",
			}, nil
		}
		return nil, err
	}
	if result == nil || result.Entry == nil {
		return &gen.VerifyWaitlistResponse{
			State:   gen.WaitlistState_WAITLIST_STATE_UNSPECIFIED,
			Message: "This verification link is invalid or no longer available.",
		}, nil
	}
	entry := result.Entry
	if result.Transitioned {
		s.emit(ctx, entry.ID, "waitlist", "waitlist.verified", "waitlist_entry", entry.ID, "")
	}
	return &gen.VerifyWaitlistResponse{
		State:   waitlistStateFromString(entry.State),
		Message: "Your email is verified. We'll contact you when access is available.",
	}, nil
}

func (s *Service) ListWaitlist(
	ctx context.Context,
	req *gen.ListWaitlistRequest,
) (*gen.ListWaitlistResponse, error) {
	var entries []*WaitlistEntry
	var next string
	err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var listErr error
		entries, next, listErr = s.store.ListWaitlistEntries(
			ctx,
			waitlistStateToString(req.State),
			req.Query,
			req.Source,
			req.Campaign,
			req.PageSize,
			req.PageToken,
		)
		return listErr
	})
	if err != nil {
		return nil, err
	}
	out := make([]*gen.WaitlistEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, waitlistEntryToProto(entry))
	}
	return &gen.ListWaitlistResponse{Entries: out, NextPageToken: next}, nil
}

func (s *Service) ReviewWaitlist(
	ctx context.Context,
	actorID string,
	req *gen.ReviewWaitlistRequest,
) (*gen.WaitlistEntry, error) {
	state := waitlistStateToString(req.State)
	if state != "approved" && state != "rejected" {
		return nil, wool.Get(ctx).NewError("review state must be approved or rejected")
	}
	var entry *WaitlistEntry
	err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var updateErr error
		entry, updateErr = s.store.UpdateWaitlistState(
			ctx, req.Id, state, req.AdminNotes, req.Tags, time.Now(),
		)
		return updateErr
	})
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, wool.Get(ctx).NewError("waitlist entry not found")
	}
	s.emit(ctx, actorID, "user", "waitlist."+state, "waitlist_entry", entry.ID, "")
	return waitlistEntryToProto(entry), nil
}

func (s *Service) InviteWaitlist(
	ctx context.Context,
	actorID string,
	req *gen.InviteWaitlistRequest,
) (*gen.WaitlistEntry, error) {
	if s.emailOutbox == nil {
		return nil, wool.Get(ctx).NewError("waitlist invitation delivery is unavailable")
	}
	var entry *WaitlistEntry
	err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var updateErr error
		entry, updateErr = s.store.InviteWaitlistEntry(ctx, req.Id, time.Now())
		if updateErr != nil || entry == nil {
			return updateErr
		}
		appBase := s.publicBaseURL(ctx)
		if appBase == "" {
			return wool.Get(ctx).NewError("public application origin is unavailable")
		}
		signupURL := appBase + "/auth/login?next=/onboarding"
		return s.emailOutbox.EnqueueTemplate(ctx, email.TemplateRequest{
			DeliveryKey: entry.ID + ":approved",
			Scope:       email.GlobalScope(),
			Source:      waitlistEmailSource,
			Template:    "waitlist_approved",
			To:          entry.Email,
			Variables:   map[string]string{"signup_url": signupURL},
			Fallback: &email.Message{
				To:       []string{entry.Email},
				Subject:  "Your access is ready",
				HTMLBody: fmt.Sprintf(`<p>Your access is ready.</p><p><a href="%s">Create your account</a></p>`, signupURL),
				TextBody: fmt.Sprintf("Your access is ready. Create your account: %s\n", signupURL),
				Tags:     map[string]string{"type": "waitlist_approved"},
			},
		})
	})
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, wool.Get(ctx).NewError("waitlist entry not found")
	}
	s.emit(ctx, actorID, "user", "waitlist.invited", "waitlist_entry", entry.ID, "")
	return waitlistEntryToProto(entry), nil
}

func (s *Service) authorizeAccountCreation(ctx context.Context, emailAddress string) error {
	if s.acquisitionMode == gen.AcquisitionMode_ACQUISITION_MODE_OPEN_SIGNUP {
		return nil
	}
	var existing *gen.User
	var invited bool
	var waitlistState string
	err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var lookupErr error
		existing, lookupErr = s.store.GetUserByEmail(ctx, emailAddress)
		var storeErr *StoreError
		if errors.As(lookupErr, &storeErr) && storeErr.StoreErrorType == ErrTypeNotFound {
			lookupErr = nil
		}
		if lookupErr != nil || existing != nil {
			return lookupErr
		}
		invited, lookupErr = s.store.HasPendingInvitationForEmail(ctx, emailAddress)
		if lookupErr != nil {
			return lookupErr
		}
		waitlistState, lookupErr = s.store.GetWaitlistStateByEmail(ctx, emailAddress)
		return lookupErr
	})
	if err != nil || existing != nil {
		return err
	}
	switch s.acquisitionMode {
	case gen.AcquisitionMode_ACQUISITION_MODE_INVITE_ONLY:
		if invited || waitlistState == "invited" {
			return nil
		}
	case gen.AcquisitionMode_ACQUISITION_MODE_APPROVAL_REQUIRED:
		if waitlistState == "approved" || waitlistState == "invited" {
			return nil
		}
	case gen.AcquisitionMode_ACQUISITION_MODE_CLOSED:
	}
	return wool.Get(ctx).NewError("account creation is not available for this address")
}

func (s *Service) authorizeAuthentication(ctx context.Context, claims *auth.Claims) error {
	resolved, err := s.store.ResolveIdentity(ctx, claims.Provider, claims.Subject)
	if err != nil {
		return err
	}
	if resolved != nil && resolved.Found {
		return nil
	}
	return s.authorizeAccountCreation(ctx, claims.Email)
}

func (s *Service) convertWaitlistLead(
	ctx context.Context,
	emailAddress, userID, orgID string,
) {
	var converted *WaitlistEntry
	_ = s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var err error
		converted, err = s.store.ConvertWaitlistEntry(
			ctx, emailAddress, userID, orgID, time.Now(),
		)
		return err
	})
	if converted != nil {
		s.emit(ctx, userID, "user", "waitlist.converted", "waitlist_entry", converted.ID, orgID)
	}
}

func newWaitlistToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(plaintext))
	return plaintext, hex.EncodeToString(hash[:]), nil
}

func newReferralCode() (string, error) {
	raw := make([]byte, 9)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func waitlistEntryToProto(entry *WaitlistEntry) *gen.WaitlistEntry {
	out := &gen.WaitlistEntry{
		Id:           entry.ID,
		Email:        entry.Email,
		Name:         entry.Name,
		Company:      entry.Company,
		UseCase:      entry.UseCase,
		State:        waitlistStateFromString(entry.State),
		Source:       entry.Source,
		Campaign:     entry.Campaign,
		Referrer:     entry.Referrer,
		ReferralCode: entry.ReferralCode,
		ReferredBy:   entry.ReferredBy,
		Tags:         append([]string(nil), entry.Tags...),
		AdminNotes:   entry.AdminNotes,
	}
	setTimestamp := func(value *time.Time) *timestamppb.Timestamp {
		if value == nil {
			return nil
		}
		return timestamppb.New(*value)
	}
	if !entry.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(entry.CreatedAt)
	}
	out.VerifiedAt = setTimestamp(entry.VerifiedAt)
	out.ApprovedAt = setTimestamp(entry.ApprovedAt)
	out.InvitedAt = setTimestamp(entry.InvitedAt)
	out.ConvertedAt = setTimestamp(entry.ConvertedAt)
	out.UnsubscribedAt = setTimestamp(entry.UnsubscribedAt)
	return out
}

func waitlistStateFromString(state string) gen.WaitlistState {
	switch state {
	case "pending":
		return gen.WaitlistState_WAITLIST_STATE_PENDING
	case "verified":
		return gen.WaitlistState_WAITLIST_STATE_VERIFIED
	case "approved":
		return gen.WaitlistState_WAITLIST_STATE_APPROVED
	case "invited":
		return gen.WaitlistState_WAITLIST_STATE_INVITED
	case "converted":
		return gen.WaitlistState_WAITLIST_STATE_CONVERTED
	case "rejected":
		return gen.WaitlistState_WAITLIST_STATE_REJECTED
	case "unsubscribed":
		return gen.WaitlistState_WAITLIST_STATE_UNSUBSCRIBED
	default:
		return gen.WaitlistState_WAITLIST_STATE_UNSPECIFIED
	}
}

func waitlistStateToString(state gen.WaitlistState) string {
	switch state {
	case gen.WaitlistState_WAITLIST_STATE_PENDING:
		return "pending"
	case gen.WaitlistState_WAITLIST_STATE_VERIFIED:
		return "verified"
	case gen.WaitlistState_WAITLIST_STATE_APPROVED:
		return "approved"
	case gen.WaitlistState_WAITLIST_STATE_INVITED:
		return "invited"
	case gen.WaitlistState_WAITLIST_STATE_CONVERTED:
		return "converted"
	case gen.WaitlistState_WAITLIST_STATE_REJECTED:
		return "rejected"
	case gen.WaitlistState_WAITLIST_STATE_UNSUBSCRIBED:
		return "unsubscribed"
	default:
		return ""
	}
}

var ErrWaitlistTokenExpired = errors.New("waitlist verification expired")
