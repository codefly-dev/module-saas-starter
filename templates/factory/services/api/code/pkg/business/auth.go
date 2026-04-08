package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"

	"backend/pkg/gen"
)

// TokenSigner abstracts JWT signing and refresh token generation.
type TokenSigner interface {
	SignAccessToken(userID, orgID, orgRole, platformRole string) (string, error)
	SignImpersonationToken(targetUserID, orgID, orgRole, platformRole, impersonatedBy string) (string, error)
	GenerateRefreshToken() (plaintext string, hash string, err error)
	JWKS() (string, error)
}

const refreshTokenTTL = 30 * 24 * time.Hour

// Authenticate exchanges a verified provider identity for access + refresh tokens.
// If the identity is unknown, the user is auto-registered.
func (s *Service) Authenticate(ctx context.Context, req *gen.AuthenticateRequest) (*gen.AuthenticateResponse, error) {
	w := wool.Get(ctx).In("Authenticate")

	if s.tokenSigner == nil {
		return nil, w.NewError("token signer not configured")
	}

	// Try to resolve existing identity
	resolved, err := s.store.ResolveIdentity(ctx, req.Provider, req.ProviderId)
	if err != nil {
		return nil, w.Wrapf(err, "cannot resolve identity")
	}

	var user *gen.User

	if !resolved.Found {
		// Auto-register: create user + identity + default org
		email := req.ProviderEmail
		if email == "" {
			email = req.ProviderId + "@" + req.Provider
		}

		regResp, err := s.RegisterUser(ctx, &gen.RegisterUserRequest{
			PrimaryEmail: email,
			Profile:      req.Profile,
			Identity: &gen.UserIdentity{
				Provider:      req.Provider,
				ProviderId:    req.ProviderId,
				ProviderEmail: email,
				EmailVerified: req.EmailVerified,
			},
		})
		if err != nil {
			return nil, w.Wrapf(err, "cannot auto-register user")
		}
		user = regResp.User

		// Re-resolve to get org + roles (RegisterUser creates them)
		resolved, err = s.store.ResolveIdentity(ctx, req.Provider, req.ProviderId)
		if err != nil {
			return nil, w.Wrapf(err, "cannot resolve after registration")
		}
	} else {
		// Fetch user for the response (minimal: just UUID + email)
		user = &gen.User{Uuid: resolved.UserID}
	}

	// Issue tokens
	accessToken, err := s.tokenSigner.SignAccessToken(resolved.UserID, resolved.OrgID, resolved.OrgRole, resolved.PlatformRole)
	if err != nil {
		return nil, w.Wrapf(err, "cannot sign access token")
	}

	refreshPlaintext, refreshHash, err := s.tokenSigner.GenerateRefreshToken()
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate refresh token")
	}

	// Create session
	session := &Session{
		ID:               uuid.New().String(),
		UserID:           resolved.UserID,
		RefreshTokenHash: refreshHash,
		FamilyID:         uuid.New().String(),
		IPAddress:        "",
		ExpiresAt:        time.Now().Add(refreshTokenTTL),
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return nil, w.Wrapf(err, "cannot create session")
	}

	s.emit(ctx, resolved.UserID, "user", "auth.login", "session", session.ID, resolved.OrgID)

	return &gen.AuthenticateResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshPlaintext,
		ExpiresIn:    900, // 15 minutes in seconds
		User:         user,
	}, nil
}

// RefreshToken exchanges a refresh token for new access + refresh tokens.
// Implements one-time-use rotation with family-based reuse detection.
func (s *Service) RefreshToken(ctx context.Context, req *gen.RefreshTokenRequest) (*gen.RefreshTokenResponse, error) {
	w := wool.Get(ctx).In("RefreshToken")

	if s.tokenSigner == nil {
		return nil, w.NewError("token signer not configured")
	}

	// Hash the incoming refresh token
	h := sha256.Sum256([]byte(req.RefreshToken))
	hash := hex.EncodeToString(h[:])

	session, err := s.store.GetSessionByRefreshTokenHash(ctx, hash)
	if err != nil {
		return nil, w.Wrapf(err, "cannot look up session")
	}
	if session == nil {
		return nil, w.NewError("invalid refresh token")
	}

	// Check if already revoked — this means reuse!
	if session.RevokedAt != nil {
		// Revoke the entire family to protect against token theft
		_ = s.store.RevokeSessionFamily(ctx, session.FamilyID, "reuse_detected")
		return nil, w.NewError("refresh token reuse detected")
	}

	// Check expiration
	if time.Now().After(session.ExpiresAt) {
		return nil, w.NewError("refresh token expired")
	}

	// Consume this refresh token (mark as rotated)
	if err := s.store.RevokeSession(ctx, session.ID, "rotated"); err != nil {
		return nil, w.Wrapf(err, "cannot revoke old session")
	}

	// Re-resolve identity to get current org role and platform role
	userID := session.UserID

	orgs, err := s.store.ListOrganizationsForUser(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list orgs")
	}
	orgID := ""
	orgRole := ""
	if len(orgs) > 0 {
		orgID = orgs[0].Id
		// Get org membership role
		members, err := s.store.ListOrgMembers(ctx, orgID)
		if err != nil {
			return nil, w.Wrapf(err, "cannot list org members")
		}
		for _, m := range members {
			if m.UserId == userID {
				orgRole = m.Role.String()
				break
			}
		}
	}

	// Check platform role
	platformRole, err := s.store.GetPlatformRole(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot get platform role")
	}

	// Issue new tokens
	accessToken, err := s.tokenSigner.SignAccessToken(userID, orgID, orgRole, platformRole)
	if err != nil {
		return nil, w.Wrapf(err, "cannot sign access token")
	}

	newRefreshPlaintext, newRefreshHash, err := s.tokenSigner.GenerateRefreshToken()
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate refresh token")
	}

	// Create new session in the same family
	newSession := &Session{
		ID:               uuid.New().String(),
		UserID:           userID,
		RefreshTokenHash: newRefreshHash,
		FamilyID:         session.FamilyID, // same family!
		IPAddress:        session.IPAddress,
		ExpiresAt:        time.Now().Add(refreshTokenTTL),
	}
	if err := s.store.CreateSession(ctx, newSession); err != nil {
		return nil, w.Wrapf(err, "cannot create new session")
	}

	return &gen.RefreshTokenResponse{
		AccessToken:  accessToken,
		RefreshToken: newRefreshPlaintext,
		ExpiresIn:    900,
	}, nil
}

// Logout revokes the session associated with the given refresh token.
func (s *Service) Logout(ctx context.Context, req *gen.LogoutRequest) error {
	w := wool.Get(ctx).In("Logout")

	h := sha256.Sum256([]byte(req.RefreshToken))
	hash := hex.EncodeToString(h[:])

	session, err := s.store.GetSessionByRefreshTokenHash(ctx, hash)
	if err != nil {
		return w.Wrapf(err, "cannot look up session")
	}
	if session == nil || session.RevokedAt != nil {
		return nil // idempotent
	}

	return s.store.RevokeSession(ctx, session.ID, "logout")
}

// GetJWKS returns the JSON Web Key Set.
func (s *Service) GetJWKS(ctx context.Context) (string, error) {
	if s.tokenSigner == nil {
		return "", wool.Get(ctx).NewError("token signer not configured")
	}
	return s.tokenSigner.JWKS()
}
