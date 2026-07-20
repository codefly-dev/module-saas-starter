// Package ed25519minter implements auth.JWTMinter backed by an Ed25519
// keypair and a pluggable SessionStore.
//
// Security properties enforced here:
//
//   - alg is locked to EdDSA. Tokens presenting any other alg (including
//     "none") are rejected at parse time.
//   - Issuer, audience, exp, nbf are validated on every verify.
//   - Access tokens carry a jti (random 128 bits) for replay correlation.
//   - Refresh tokens are opaque 256-bit tokens, SHA-256 hashed at rest,
//     compared constant-time via crypto/subtle.
//   - Refresh consumption and successor insertion are one atomic store
//     operation. Reuse commits revocation of every active session for the
//     affected user before auth.ErrRefreshReuse is returned.
//   - Clock skew tolerance is configurable, default 60s.
//
// This package does NOT own key management. Callers pass a keypair that
// typically came from Vault. Rotation is handled by constructing a new
// Minter instance from the new key; sidecars that need to verify both old
// and new tokens can hold multiple Verifier instances (separate type).
package ed25519minter

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"accounts/pkg/auth"
	"accounts/pkg/business"
)

// Config controls Minter behaviour. Zero values are safe defaults.
type Config struct {
	// Issuer is set as `iss` on every access token.
	Issuer string
	// Audience is set as `aud` on every access token.
	Audience string
	// AccessTokenTTL is the lifetime of an access token. Default 15 min.
	AccessTokenTTL time.Duration
	// ImpersonationTokenTTL caps the lifetime of access tokens minted
	// for impersonation sessions (acting claim non-empty). Default 5 min.
	// Falls back to AccessTokenTTL when zero. Belt-and-suspenders against
	// admin "view-as" sessions left open in a forgotten tab.
	ImpersonationTokenTTL time.Duration
	// SessionPolicy controls absolute, idle, and active-device limits.
	SessionPolicy auth.SessionPolicy
	// ClockSkew is the tolerance allowed when validating exp/nbf. Default 60s.
	ClockSkew time.Duration
}

func (c *Config) withDefaults() error {
	if c.Issuer == "" {
		c.Issuer = "saas-starter"
	}
	if c.Audience == "" {
		c.Audience = "saas-starter"
	}
	if c.AccessTokenTTL == 0 {
		c.AccessTokenTTL = 15 * time.Minute
	}
	if c.ImpersonationTokenTTL == 0 {
		c.ImpersonationTokenTTL = 5 * time.Minute
	}
	if c.ClockSkew == 0 {
		c.ClockSkew = 60 * time.Second
	}
	c.SessionPolicy = c.SessionPolicy.WithDefaults()
	return c.SessionPolicy.Validate()
}

// accessClaims is the exact claim shape of our access token. Field names use
// short JSON keys to keep tokens compact; they are NOT part of any external
// API surface.
type accessClaims struct {
	jwt.RegisteredClaims
	OrgID                 string           `json:"org,omitempty"`
	OrgRole               string           `json:"or,omitempty"`
	PlatformRole          string           `json:"pr,omitempty"`
	SessionID             string           `json:"sid"`
	ActingAsUserID        string           `json:"acting,omitempty"`
	AuthenticationMethods []string         `json:"amr,omitempty"`
	AuthenticationTime    *jwt.NumericDate `json:"auth_time,omitempty"`
	AssuranceLevel        string           `json:"acr,omitempty"`
	MFAVerifiedAt         *jwt.NumericDate `json:"mfa_at,omitempty"`
	// MFASatisfied marks tokens minted after a successful MFA challenge
	// (or by users who haven't enrolled MFA — the gate is "no enrolled
	// device" OR "satisfied this session", not "always satisfied"). When
	// false, requireMFA(ctx) blocks sensitive operations.
	MFASatisfied bool `json:"mfa,omitempty"`
}

// Minter is the concrete JWTMinter. Construct via New.
type Minter struct {
	cfg        Config
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	keyID      string
	store      auth.SessionStore
	revoker    auth.TokenRevoker
	now        func() time.Time // injectable clock for tests
	configErr  error
}

// New constructs a Minter. The priv key is used for signing and the pub key
// for verifying locally-minted tokens (Mint → VerifyAccess roundtrip). The
// sidecar runs its own verifier with a different lifecycle.
//
// revoker may be nil — falls back to NoopTokenRevoker (TTL-only revocation,
// matching the pre-Redis behaviour). Wire it in production for real
// access-token revocation on logout.
func New(cfg Config, priv ed25519.PrivateKey, store auth.SessionStore) *Minter {
	configErr := cfg.withDefaults()
	pub := priv.Public().(ed25519.PublicKey)
	h := sha256.Sum256(pub)
	keyID := base64.RawURLEncoding.EncodeToString(h[:8])
	return &Minter{
		cfg:        cfg,
		privateKey: priv,
		publicKey:  pub,
		keyID:      keyID,
		store:      store,
		revoker:    auth.NoopTokenRevoker{},
		now:        time.Now,
		configErr:  configErr,
	}
}

// SetRevoker wires an access-token revocation list. Required for real
// logout (otherwise old access tokens stay valid until natural expiry).
// Pass auth.NoopTokenRevoker (the default) to opt out.
func (m *Minter) SetRevoker(r auth.TokenRevoker) {
	if r != nil {
		m.revoker = r
	}
}

// KeyID returns the deterministic kid header. Sidecar caches verifiers by this
// value to support key rotation.
func (m *Minter) KeyID() string { return m.keyID }

// PublicKey exposes the Ed25519 public key for out-of-band distribution
// (e.g. writing a JWKS file for the sidecar).
func (m *Minter) PublicKey() ed25519.PublicKey { return m.publicKey }

// JWKS returns a JSON Web Key Set containing this minter's public key.
// Used by external tooling; the sidecar loads its key from Vault directly.
func (m *Minter) JWKS() (string, error) {
	x := base64.RawURLEncoding.EncodeToString(m.publicKey)
	keys := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "OKP",
				"crv": "Ed25519",
				"alg": "EdDSA",
				"use": "sig",
				"kid": m.keyID,
				"x":   x,
			},
		},
	}
	buf, err := json.Marshal(keys)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// Mint implements auth.JWTMinter.Mint. It issues a fresh access token and
// refresh token, persisting the session row with a new family_id.
//
// For refresh rotation (issuing a new refresh within an existing family),
// callers go through VerifyRefresh first, which mints a rotated token.
func (m *Minter) Mint(ctx context.Context, identity *auth.Identity) (*auth.TokenPair, error) {
	if m.configErr != nil {
		return nil, fmt.Errorf("ed25519minter: invalid session policy: %w", m.configErr)
	}
	return m.mint(ctx, identity, uuid.UUID{} /* new family */)
}

// mint is the shared implementation for both initial Mint and rotation.
// familyID == zero means "start a new family".
func (m *Minter) mint(ctx context.Context, identity *auth.Identity, familyID uuid.UUID) (*auth.TokenPair, error) {
	pair, rec, err := m.prepareMint(identity, familyID)
	if err != nil {
		return nil, err
	}
	if err := m.store.Insert(ctx, rec); err != nil {
		return nil, fmt.Errorf("ed25519minter: insert session: %w", err)
	}
	return pair, nil
}

// prepareMint performs the cryptographic half of token issuance without
// writing session state. Initial login persists the returned record through
// Insert; refresh passes it to SessionStore.RotateRefresh so consumption and
// successor insertion share one database transaction.
func (m *Minter) prepareMint(identity *auth.Identity, familyID uuid.UUID) (*auth.TokenPair, *auth.SessionRecord, error) {
	if identity == nil || identity.UserID == uuid.Nil {
		return nil, nil, fmt.Errorf("ed25519minter: identity missing user id")
	}
	if identity.AssuranceLevel == "" {
		identity.AssuranceLevel = auth.AssuranceLevelAAL1
	}

	now := m.now()
	sessionID := identity.SessionID
	if sessionID == uuid.Nil {
		sessionID = business.NewID()
	}
	if familyID == uuid.Nil {
		familyID = business.NewID()
	}

	// Access token
	access, err := m.signAccess(identity, sessionID, now)
	if err != nil {
		return nil, nil, fmt.Errorf("ed25519minter: sign access: %w", err)
	}

	// Refresh token
	plain, hash, err := newRefreshToken()
	if err != nil {
		return nil, nil, fmt.Errorf("ed25519minter: generate refresh: %w", err)
	}

	idleExpiresAt := now.Add(m.cfg.SessionPolicy.IdleTimeout)
	absoluteExpiresAt := now.Add(m.cfg.SessionPolicy.AbsoluteLifetime)
	if idleExpiresAt.After(absoluteExpiresAt) {
		idleExpiresAt = absoluteExpiresAt
	}
	rec := &auth.SessionRecord{
		// The sid claim and persisted session primary key must identify the
		// same session for audit, revocation, and user-facing device lists.
		ID:                    sessionID,
		UserID:                identity.UserID,
		OrgID:                 identity.OrgID,
		OrgRole:               identity.OrgRole,
		PlatformRole:          identity.PlatformRole,
		MFASatisfied:          identity.MFASatisfied,
		AuthenticationMethods: identity.AuthenticationMethods,
		AuthenticatedAt:       identity.AuthenticatedAt,
		AssuranceLevel:        identity.AssuranceLevel,
		MFAVerifiedAt:         identity.MFAVerifiedAt,
		DeviceInfo:            maps.Clone(identity.DeviceInfo),
		IPAddress:             identity.IPAddress,
		FamilyID:              familyID,
		RefreshHash:           hash,
		IssuedAt:              now,
		LastActiveAt:          now,
		IdleExpiresAt:         idleExpiresAt,
		ExpiresAt:             absoluteExpiresAt,
	}
	return &auth.TokenPair{AccessToken: access, RefreshToken: plain}, rec, nil
}

// VerifyRefresh implements auth.JWTMinter.VerifyRefresh with OWASP rotation.
//
// Behaviour:
//   - Valid unexpired active refresh → rotate: revoke this row, mint a new
//     row in the same family, return fresh (access, refresh) pair.
//   - Submitted hash maps to a row consumed by rotation → reuse. Revoke every
//     active session for the user and return ErrRefreshReuse.
//   - Submitted hash maps to a row revoked by logout or an authorization
//     mutation → return ErrRefreshRevoked without triggering replay handling.
//   - Expired or authorization-rejected sessions are terminally revoked;
//     unknown tokens return the same ErrRefreshRevoked sentinel to avoid an
//     existence oracle.
func (m *Minter) VerifyRefresh(ctx context.Context, refreshToken string) (*auth.TokenPair, error) {
	if m.configErr != nil {
		return nil, fmt.Errorf("ed25519minter: invalid session policy: %w", m.configErr)
	}
	hash := hashRefresh(refreshToken)
	var pair *auth.TokenPair
	err := m.store.RotateRefresh(ctx, hash, func(
		rec *auth.SessionRecord,
		authorization auth.RefreshAuthorization,
	) (*auth.SessionRecord, error) {
		// Constant-time equality remains defence in depth for alternative stores
		// whose lookup implementation is not the indexed Postgres operation.
		if subtle.ConstantTimeCompare(rec.RefreshHash, hash) != 1 {
			return nil, auth.RejectRefresh(auth.RefreshRejectionHashMismatch)
		}
		now := m.now()
		if !now.Before(rec.ExpiresAt) {
			return nil, auth.RejectRefresh(auth.RefreshRejectionAbsoluteLifetime)
		}
		if !now.Before(rec.IdleExpiresAt) {
			return nil, auth.RejectRefresh(auth.RefreshRejectionIdleTimeout)
		}

		identity, err := identityFromCurrentAuthorization(rec, authorization, now, business.NewID())
		if err != nil {
			return nil, err
		}

		var next *auth.SessionRecord
		pair, next, err = m.prepareMint(identity, rec.FamilyID)
		if next != nil {
			// Rotation advances activity but never slides the device family's
			// creation time or absolute expiry.
			next.IssuedAt = rec.IssuedAt
			next.LastActiveAt = now
			next.ExpiresAt = rec.ExpiresAt
			next.IdleExpiresAt = now.Add(m.cfg.SessionPolicy.IdleTimeout)
			if next.IdleExpiresAt.After(rec.ExpiresAt) {
				next.IdleExpiresAt = rec.ExpiresAt
			}
		}
		return next, err
	})
	if err != nil {
		if errors.Is(err, auth.ErrRefreshRevoked) || errors.Is(err, auth.ErrRefreshReuse) {
			return nil, err
		}
		return nil, fmt.Errorf("ed25519minter: rotate refresh: %w", err)
	}
	if pair == nil {
		return nil, errors.New("ed25519minter: refresh rotation returned no token pair")
	}
	return pair, nil
}

// SwitchOrganization issues a new access-token projection for an existing
// active device session. It deliberately does not consume or replace the
// refresh token: organization selection is serialized on the session row and
// cannot be mistaken for refresh-token replay when it races a background
// refresh in another browser task.
func (m *Minter) SwitchOrganization(
	ctx context.Context,
	userID uuid.UUID,
	sessionID uuid.UUID,
	organizationID uuid.UUID,
) (string, error) {
	if m.configErr != nil {
		return "", fmt.Errorf("ed25519minter: invalid session policy: %w", m.configErr)
	}
	if userID == uuid.Nil || sessionID == uuid.Nil || organizationID == uuid.Nil {
		return "", auth.ErrSessionUnavailable
	}

	var accessToken string
	err := m.store.ExchangeOrganization(ctx, userID, sessionID, organizationID, func(
		current *auth.SessionRecord,
		authorization auth.RefreshAuthorization,
	) error {
		now := m.now()
		identity, err := identityFromCurrentAuthorization(current, authorization, now, current.ID)
		if err != nil {
			return err
		}
		accessToken, err = m.signAccess(identity, current.ID, now)
		return err
	})
	if err != nil {
		if errors.Is(err, auth.ErrSessionUnavailable) || errors.Is(err, auth.ErrOrganizationAccessDenied) {
			return "", err
		}
		return "", fmt.Errorf("ed25519minter: switch organization: %w", err)
	}
	if accessToken == "" {
		return "", errors.New("ed25519minter: organization exchange returned no access token")
	}
	return accessToken, nil
}

func identityFromCurrentAuthorization(
	rec *auth.SessionRecord,
	authorization auth.RefreshAuthorization,
	now time.Time,
	sessionID uuid.UUID,
) (*auth.Identity, error) {
	assurance := auth.Assurance{
		AuthenticationMethods: rec.AuthenticationMethods,
		AuthenticatedAt:       rec.AuthenticatedAt,
		Level:                 rec.AssuranceLevel,
		MFAVerifiedAt:         rec.MFAVerifiedAt,
	}
	if authorization.MFAEnrolled &&
		(!rec.MFASatisfied || !assurance.HasMFAEvidence() || rec.MFAVerifiedAt.After(now.Add(time.Minute))) {
		return nil, auth.RejectRefresh(auth.RefreshRejectionMFAReauthentication)
	}

	authenticationMethods := slices.Clone(rec.AuthenticationMethods)
	assuranceLevel := rec.AssuranceLevel
	mfaVerifiedAt := rec.MFAVerifiedAt
	mfaSatisfied := rec.MFASatisfied
	if !authorization.MFAEnrolled {
		authenticationMethods = slices.DeleteFunc(authenticationMethods, isMFAMethod)
		assuranceLevel = auth.AssuranceLevelAAL1
		mfaVerifiedAt = time.Time{}
		mfaSatisfied = true
	}

	return &auth.Identity{
		UserID:                rec.UserID,
		OrgID:                 authorization.OrgID,
		OrgRole:               authorization.OrgRole,
		PlatformRole:          authorization.PlatformRole,
		SessionID:             sessionID,
		MFASatisfied:          mfaSatisfied,
		AuthenticationMethods: authenticationMethods,
		AuthenticatedAt:       rec.AuthenticatedAt,
		AssuranceLevel:        assuranceLevel,
		MFAVerifiedAt:         mfaVerifiedAt,
		DeviceInfo:            maps.Clone(rec.DeviceInfo),
		IPAddress:             rec.IPAddress,
	}, nil
}

func isMFAMethod(method string) bool {
	return method == auth.AuthenticationMethodOTP ||
		method == auth.AuthenticationMethodRecovery ||
		method == auth.AuthenticationMethodWebAuthn
}

// Revoke implements auth.JWTMinter.Revoke by revoking the family backing the
// given refresh token. Safe to call with an already-revoked token.
func (m *Minter) Revoke(ctx context.Context, refreshToken string) error {
	hash := hashRefresh(refreshToken)
	rec, err := m.store.FindByRefreshHash(ctx, hash)
	if err != nil {
		if errors.Is(err, auth.ErrRefreshRevoked) {
			return nil // idempotent: already gone
		}
		return fmt.Errorf("ed25519minter: revoke lookup: %w", err)
	}
	return m.store.RevokeFamily(ctx, rec.FamilyID, "logout")
}

// VerifyAccess parses and validates an access token minted by this Minter.
// Used by tests and the backend's own refresh endpoint. The sidecar has its
// own verifier that doesn't depend on Minter.
//
// alg is locked to EdDSA. iss/aud/exp/nbf are validated. Clock skew tolerated.
func (m *Minter) VerifyAccess(tokenString string) (*auth.Identity, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(m.cfg.Issuer),
		jwt.WithAudience(m.cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(m.cfg.ClockSkew),
	)
	claims := &accessClaims{}
	token, err := parser.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "EdDSA" {
			return nil, auth.ErrTokenAlgForbidden
		}
		return m.publicKey, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return nil, auth.ErrTokenExpired
		case errors.Is(err, jwt.ErrTokenInvalidIssuer):
			return nil, auth.ErrTokenWrongIssuer
		case errors.Is(err, jwt.ErrTokenInvalidAudience):
			return nil, auth.ErrTokenWrongAudience
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			return nil, auth.ErrTokenSignature
		case errors.Is(err, auth.ErrTokenAlgForbidden):
			return nil, auth.ErrTokenAlgForbidden
		default:
			return nil, fmt.Errorf("%w: %v", auth.ErrTokenMalformed, err)
		}
	}
	if !token.Valid {
		return nil, auth.ErrTokenMalformed
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("%w: sub: %v", auth.ErrTokenMalformed, err)
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: sid: %v", auth.ErrTokenMalformed, err)
	}
	var orgID uuid.UUID
	if claims.OrgID != "" {
		orgID, err = uuid.Parse(claims.OrgID)
		if err != nil {
			return nil, fmt.Errorf("%w: org: %v", auth.ErrTokenMalformed, err)
		}
	}
	var actingAs uuid.UUID
	if claims.ActingAsUserID != "" {
		actingAs, err = uuid.Parse(claims.ActingAsUserID)
		if err != nil {
			return nil, fmt.Errorf("%w: acting: %v", auth.ErrTokenMalformed, err)
		}
	}

	// Access-token revocation (logout / explicit kill). Checked AFTER
	// signature + claim validation so an unsigned/expired token still
	// fails fast without a backing-store call. NoopTokenRevoker returns
	// false here so the dev path is unchanged.
	if claims.ID != "" && m.revoker.IsRevoked(context.Background(), claims.ID) {
		return nil, auth.ErrTokenRevoked
	}

	return &auth.Identity{
		UserID:                userID,
		OrgID:                 orgID,
		OrgRole:               claims.OrgRole,
		PlatformRole:          claims.PlatformRole,
		SessionID:             sessionID,
		ActingAsUserID:        actingAs,
		MFASatisfied:          claims.MFASatisfied,
		AuthenticationMethods: claims.AuthenticationMethods,
		AuthenticatedAt:       numericDateTime(claims.AuthenticationTime),
		AssuranceLevel:        claims.AssuranceLevel,
		MFAVerifiedAt:         numericDateTime(claims.MFAVerifiedAt),
	}, nil
}

// RevokeAccess parses the given access token (signature + claim
// validation included) and adds its jti to the revocation list with
// TTL = remaining lifetime. Returns nil for tokens that are already
// expired or otherwise unparseable — logout is best-effort, never
// fails the request because the access half couldn't be revoked.
func (m *Minter) RevokeAccess(ctx context.Context, accessToken string) error {
	identity, err := m.VerifyAccess(accessToken)
	if err != nil {
		return nil //nolint:nilerr // best-effort revocation
	}
	_ = identity // identity is built but not needed here

	// Re-parse just to grab jti + exp without re-verifying signature.
	// VerifyAccess above already validated; this parse is unverified
	// but safe — claim values are advisory at this point.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	claims := &accessClaims{}
	if _, _, perr := parser.ParseUnverified(accessToken, claims); perr != nil {
		return nil //nolint:nilerr // already validated; fall through silently
	}
	if claims.ID == "" || claims.ExpiresAt == nil {
		return nil
	}
	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl <= 0 {
		return nil
	}
	return m.revoker.Revoke(ctx, claims.ID, ttl)
}

func (m *Minter) signAccess(identity *auth.Identity, sessionID uuid.UUID, now time.Time) (string, error) {
	jti, err := randHex(16)
	if err != nil {
		return "", err
	}
	// Impersonation sessions get a shorter TTL — the worst-case window
	// for an admin walking away from a "viewing as customer" session is
	// capped at 5 min, vs 15 min for normal sessions. The impersonation
	// banner makes the state visible; this is belt-and-suspenders.
	ttl := m.cfg.AccessTokenTTL
	if identity.ActingAsUserID != uuid.Nil {
		if impTTL := m.cfg.ImpersonationTokenTTL; impTTL > 0 && impTTL < ttl {
			ttl = impTTL
		}
	}
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.cfg.Issuer,
			Subject:   identity.UserID.String(),
			Audience:  jwt.ClaimStrings{m.cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-1 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        jti,
		},
		SessionID: sessionID.String(),
	}
	if identity.OrgID != uuid.Nil {
		claims.OrgID = identity.OrgID.String()
		claims.OrgRole = identity.OrgRole
	}
	claims.PlatformRole = identity.PlatformRole
	if identity.ActingAsUserID != uuid.Nil {
		claims.ActingAsUserID = identity.ActingAsUserID.String()
	}
	claims.MFASatisfied = identity.MFASatisfied
	claims.AuthenticationMethods = identity.AuthenticationMethods
	if !identity.AuthenticatedAt.IsZero() {
		claims.AuthenticationTime = jwt.NewNumericDate(identity.AuthenticatedAt)
	}
	claims.AssuranceLevel = identity.AssuranceLevel
	if !identity.MFAVerifiedAt.IsZero() {
		claims.MFAVerifiedAt = jwt.NewNumericDate(identity.MFAVerifiedAt)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = m.keyID
	return token.SignedString(m.privateKey)
}

func numericDateTime(value *jwt.NumericDate) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.Time
}

// newRefreshToken generates a cryptographically random opaque refresh token
// and returns (base64url plaintext, SHA-256 hash).
func newRefreshToken() (plaintext string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(plaintext))
	return plaintext, h[:], nil
}

// hashRefresh returns the SHA-256 hash of a refresh token plaintext.
func hashRefresh(plaintext string) []byte {
	h := sha256.Sum256([]byte(plaintext))
	return h[:]
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateKey returns a fresh Ed25519 keypair. Use in tests and in local dev
// when no Vault is available.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// Compile-time assertion that Minter satisfies the interface.
var _ auth.JWTMinter = (*Minter)(nil)
