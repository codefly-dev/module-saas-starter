package infra

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/codefly-dev/core/wool"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/golang-jwt/jwt/v5"
)

const (
	AccessTokenTTL = 15 * time.Minute
	TokenIssuer    = "codefly-user-mgmt"
)

// AccessClaims are the JWT claims embedded in access tokens.
type AccessClaims struct {
	jwt.RegisteredClaims
	OrgID          string   `json:"org,omitempty"`
	OrgRole        string   `json:"org_role,omitempty"`        // "owner"|"admin"|"member"
	PlatformRole   string   `json:"platform_role,omitempty"`   // "super_admin"|"support"|"billing"|absent
	ImpersonatedBy string   `json:"impersonated_by,omitempty"` // platform admin who initiated impersonation
	Roles          []string `json:"roles,omitempty"`           // deprecated, kept for backward compat
}

// TokenService handles JWT signing/verification and refresh token generation.
type TokenService struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	keyID      string
}

// NewTokenService creates a TokenService by fetching the Ed25519 key from Vault KV.
func NewTokenService(ctx context.Context) (*TokenService, error) {
	w := wool.Get(ctx).In("NewTokenService")

	vaultAddr, err := codefly.For(ctx).Service("vault").Configuration("vault", "address")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get vault address")
	}
	vaultToken, err := codefly.For(ctx).Service("vault").Secret("vault", "token")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get vault token")
	}

	// Fetch key from Vault KV v2
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vaultAddr+"/v1/secret/data/jwt-signing-key", nil)
	if err != nil {
		return nil, w.Wrapf(err, "cannot create vault request")
	}
	req.Header.Set("X-Vault-Token", vaultToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, w.Wrapf(err, "cannot fetch JWT key from vault")
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, w.NewError("vault returned %d fetching JWT key: %s", resp.StatusCode, string(body))
	}

	var envelope struct {
		Data struct {
			Data struct {
				PrivateKey string `json:"private_key"`
				PublicKey  string `json:"public_key"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, w.Wrapf(err, "cannot parse vault response")
	}

	seed, err := base64.StdEncoding.DecodeString(envelope.Data.Data.PrivateKey)
	if err != nil {
		return nil, w.Wrapf(err, "cannot decode private key")
	}
	pub, err := base64.StdEncoding.DecodeString(envelope.Data.Data.PublicKey)
	if err != nil {
		return nil, w.Wrapf(err, "cannot decode public key")
	}

	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := ed25519.PublicKey(pub)

	// Key ID = first 8 bytes of SHA-256 of public key, base64url
	h := sha256.Sum256(publicKey)
	keyID := base64.RawURLEncoding.EncodeToString(h[:8])

	w.Debug("JWT token service initialized", wool.Field("keyID", keyID))

	return &TokenService{
		privateKey: privateKey,
		publicKey:  publicKey,
		keyID:      keyID,
	}, nil
}

// NewTokenServiceLocal creates a TokenService with an ephemeral Ed25519 key pair (for local/test use).
func NewTokenServiceLocal() (*TokenService, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("cannot generate Ed25519 key: %w", err)
	}
	h := sha256.Sum256(pub)
	keyID := base64.RawURLEncoding.EncodeToString(h[:8])
	return &TokenService{
		privateKey: priv,
		publicKey:  pub,
		keyID:      keyID,
	}, nil
}

// SignAccessToken creates a signed JWT access token.
func (t *TokenService) SignAccessToken(userID, orgID, orgRole, platformRole string) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    TokenIssuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
			ID:        fmt.Sprintf("%x", mustRandBytes(16)),
		},
		OrgID:        orgID,
		OrgRole:      orgRole,
		PlatformRole: platformRole,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = t.keyID
	return token.SignedString(t.privateKey)
}

// SignImpersonationToken creates a JWT for an impersonated user, recording who initiated it.
func (t *TokenService) SignImpersonationToken(targetUserID, orgID, orgRole, platformRole, impersonatedBy string) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    TokenIssuer,
			Subject:   targetUserID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
			ID:        fmt.Sprintf("%x", mustRandBytes(16)),
		},
		OrgID:          orgID,
		OrgRole:        orgRole,
		PlatformRole:   platformRole,
		ImpersonatedBy: impersonatedBy,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = t.keyID
	return token.SignedString(t.privateKey)
}

// VerifyAccessToken parses and validates a JWT, returning the claims.
func (t *TokenService) VerifyAccessToken(tokenString string) (*AccessClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &AccessClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return t.publicKey, nil
	},
		jwt.WithIssuer(TokenIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods([]string{"EdDSA"}),
	)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*AccessClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

// GenerateRefreshToken creates a cryptographically random opaque token and its SHA-256 hash.
func (t *TokenService) GenerateRefreshToken() (plaintext string, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(plaintext))
	hash = hex.EncodeToString(h[:])
	return plaintext, hash, nil
}

// JWKS returns the JSON Web Key Set containing the public key.
func (t *TokenService) JWKS() (string, error) {
	x := base64.RawURLEncoding.EncodeToString(t.publicKey)

	jwks := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "OKP",
				"crv": "Ed25519",
				"x":   x,
				"kid": t.keyID,
				"use": "sig",
				"alg": "EdDSA",
			},
		},
	}

	data, err := json.Marshal(jwks)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// PublicKey returns the Ed25519 public key for direct use (e.g., by sidecar).
func (t *TokenService) PublicKey() ed25519.PublicKey {
	return t.publicKey
}

func mustRandBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
