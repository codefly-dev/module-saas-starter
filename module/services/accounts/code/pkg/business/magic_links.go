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

	"accounts/pkg/auth"
	"accounts/pkg/email"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const magicLinkTTL = 15 * time.Minute

const magicLinkEmailSource = "saas.accounts.authentication"

// MagicLink is the domain representation of a passwordless magic link.
type MagicLink struct {
	ID        string
	Email     string
	TokenHash string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// SendMagicLink generates a random token, hashes it, stores the hash in the
// DB, and sends an email with a link containing the plaintext token.
func (s *Service) SendMagicLink(ctx context.Context, emailAddr string) error {
	w := wool.Get(ctx).In("SendMagicLink")

	// Generate random 32-byte token.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return w.Wrapf(err, "cannot generate token")
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(plaintext))
	tokenHash := hex.EncodeToString(h[:])

	ml := &MagicLink{
		ID:        NewIDString(),
		Email:     emailAddr,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(magicLinkTTL),
	}

	// Build the link URL.
	appBase := s.publicBaseURL(ctx)
	if appBase == "" {
		return w.NewError("public application origin is unavailable")
	}
	linkURL := fmt.Sprintf("%s/auth/magic-link?token=%s", appBase, plaintext)

	// Pre-auth: no user/org identity exists at send time, so the token row and
	// exact global email job share the audited control-plane transaction.
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		if err := s.store.CreateMagicLink(ctx, ml); err != nil {
			return err
		}
		if s.emailOutbox == nil {
			return nil
		}
		return s.emailOutbox.EnqueueTemplate(ctx, email.TemplateRequest{
			DeliveryKey: ml.ID,
			Scope:       email.GlobalScope(),
			Source:      magicLinkEmailSource,
			Template:    "magic_link",
			To:          emailAddr,
			Variables:   map[string]string{"link_url": linkURL},
			Fallback: &email.Message{
				To:       []string{emailAddr},
				Subject:  "Your sign-in link",
				HTMLBody: renderMagicLinkHTML(linkURL),
				TextBody: renderMagicLinkText(linkURL),
				Tags:     map[string]string{"type": "magic_link"},
			},
		})
	}); err != nil {
		return w.Wrapf(err, "cannot store magic link and delivery job")
	}

	return nil
}

// VerifyMagicLink hashes the provided token, looks it up in the DB,
// verifies it is not expired or already used, marks it as used, finds
// or creates the user by email, and returns an AuthenticateResponse.
func (s *Service) VerifyMagicLink(ctx context.Context, token string) (*gen.AuthenticateResponse, error) {
	w := wool.Get(ctx).In("VerifyMagicLink")

	if s.resolver == nil || s.minter == nil {
		return nil, w.NewError("auth path not wired: resolver/minter missing")
	}

	h := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(h[:])

	var ml *MagicLink
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var e error
		ml, e = s.store.GetMagicLinkByTokenHash(ctx, tokenHash)
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot look up magic link")
	}
	if ml == nil {
		return nil, w.NewError("invalid magic link token")
	}
	if ml.UsedAt != nil {
		return nil, w.NewError("magic link already used")
	}
	if time.Now().After(ml.ExpiresAt) {
		return nil, w.NewError("magic link has expired")
	}

	// Mark as used.
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		return s.store.MarkMagicLinkUsed(ctx, ml.ID)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot mark magic link as used")
	}

	// Synthesize claims using the email as both provider subject and email.
	// The "magic_link" provider + email-as-subject ensures JIT provisioning
	// via the standard Resolve pipeline; a verified magic link is itself proof
	// of address ownership, so it resolves as a signup.
	claims := &auth.Claims{
		Provider:  "magic_link",
		Subject:   ml.Email,
		Email:     ml.Email,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	identity, err := s.resolver.Resolve(ctx, claims, auth.SignupIntent{})
	if err != nil {
		return nil, w.Wrapf(err, "cannot resolve identity for magic link")
	}
	identity.AuthenticationMethods = []string{auth.AuthenticationMethodEmail}
	identity.AuthenticatedAt = time.Now()
	identity.AssuranceLevel = auth.AssuranceLevelAAL1
	var enrolled bool
	var user *gen.User
	if err := s.store.WithUserTx(ctx, identity.UserID.String(), func(ctx context.Context) error {
		var err error
		enrolled, err = s.store.HasVerifiedMFA(ctx, identity.UserID.String())
		if err != nil {
			return err
		}
		user, err = s.store.GetUser(ctx, identity.UserID.String())
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot determine MFA enrollment")
	}
	if user == nil {
		user = &gen.User{Uuid: identity.UserID.String()}
	}
	if enrolled {
		mfaToken, err := s.BeginMFALogin(ctx, identity)
		if err != nil {
			return nil, w.Wrapf(err, "begin MFA login")
		}
		return &gen.AuthenticateResponse{User: user, MfaRequired: true, MfaToken: mfaToken}, nil
	}
	identity.MFASatisfied = true

	pair, err := s.minter.Mint(ctx, identity)
	if err != nil {
		return nil, w.Wrapf(err, "mint tokens")
	}

	s.emit(ctx, identity.UserID.String(), "user", "auth.magic_link_login",
		"session", identity.SessionID.String(), identity.OrgID.String())

	return &gen.AuthenticateResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    int64(AccessTokenLifetime.Seconds()),
		User:         user,
	}, nil
}

func renderMagicLinkHTML(linkURL string) string {
	return fmt.Sprintf(`<!doctype html>
<html>
<body style="font-family: system-ui, sans-serif; max-width: 560px; margin: 40px auto; padding: 24px;">
<h2>Sign in to your account</h2>
<p>Click the button below to sign in. This link expires in 15 minutes.</p>
<p>
  <a href="%s" style="display:inline-block; padding:12px 24px; background:#0066cc; color:white; text-decoration:none; border-radius:6px;">
    Sign in
  </a>
</p>
<p style="color:#666; font-size:14px;">If you didn't request this link, you can safely ignore this email.</p>
</body>
</html>`, linkURL)
}

func renderMagicLinkText(linkURL string) string {
	return fmt.Sprintf(`Sign in to your account

Click the link below to sign in (expires in 15 minutes):
%s

If you didn't request this link, you can safely ignore this email.
`, linkURL)
}
