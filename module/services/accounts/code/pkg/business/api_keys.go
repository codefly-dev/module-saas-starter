package business

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// KeyHasher hashes API key plaintext into a storable hash.
type KeyHasher interface {
	HashKey(ctx context.Context, plaintext string) (string, error)
}

// CreateAPIKey generates a new API key, hashes it via vault, and stores the hash.
func (s *Service) CreateAPIKey(ctx context.Context, userID string, req *gen.CreateAPIKeyRequest) (*gen.CreateAPIKeyResponse, error) {
	w := wool.Get(ctx).In("CreateAPIKey")

	if s.hasher == nil {
		return nil, w.NewError("key hasher not configured")
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.AsTime().After(time.Now()) {
		return nil, w.NewError("API key expiration must be in the future")
	}

	// 32 base62 characters ≈ 190 bits of entropy.
	encoded, err := randomBase62(32)
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate random key")
	}

	// Format: cfly_sk_{env}_{base62}
	envPrefix := "live"
	if req.Environment == gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST {
		envPrefix = "test"
	}
	plaintext := fmt.Sprintf("cfly_sk_%s_%s", envPrefix, encoded)
	prefix := plaintext[:12]

	// Hash via vault transit
	keyHash, err := s.hasher.HashKey(ctx, plaintext)
	if err != nil {
		return nil, w.Wrapf(err, "cannot hash key")
	}

	keyID := NewIDString()
	key := &gen.APIKey{
		Id:             keyID,
		OrganizationId: req.OrganizationId,
		UserId:         userID,
		Name:           req.Name,
		Prefix:         prefix,
		Scopes:         req.Scopes,
		Environment:    req.Environment,
		ExpiresAt:      req.ExpiresAt,
	}

	if err := s.store.WithOrgTx(ctx, req.OrganizationId, func(ctx context.Context) error {
		quota, err := s.cardinalityQuotaInTx(ctx, req.OrganizationId, EntitlementAPIKeys)
		if err != nil {
			return w.Wrapf(err, "cannot check API key quota")
		}
		if err := quota.RequireAvailable(); err != nil {
			return err
		}
		return s.store.CreateAPIKey(ctx, key, keyHash)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot store API key")
	}

	s.emit(ctx, userID, "user", EventAPIKeyCreated, "api_key", keyID, req.OrganizationId)

	return &gen.CreateAPIKeyResponse{
		Key:          key,
		PlaintextKey: plaintext,
	}, nil
}

// ValidateAPIKey checks a hashed key against the store.
//
// The plaintext key is presented before a request principal exists. The store
// exposes one narrow bootstrap capability that resolves the key and its current
// owner policy facts atomically; business code never receives a raw pool or a
// general cross-tenant transaction.
func (s *Service) ValidateAPIKey(ctx context.Context, plaintextKey string) (*gen.ValidateAPIKeyResponse, error) {
	w := wool.Get(ctx).In("ValidateAPIKey")

	if s.hasher == nil {
		return nil, w.NewError("key hasher not configured")
	}

	keyHash, err := s.hasher.HashKey(ctx, plaintextKey)
	if err != nil {
		return nil, w.Wrapf(err, "cannot hash key")
	}

	authentication, err := s.store.GetAPIKeyAuthentication(ctx, keyHash)
	if err != nil {
		return nil, w.Wrapf(err, "cannot look up key")
	}
	if authentication == nil || authentication.Key == nil {
		return &gen.ValidateAPIKeyResponse{Valid: false}, nil
	}
	key := authentication.Key

	// Check revoked
	if key.RevokedAt != nil {
		return &gen.ValidateAPIKeyResponse{Valid: false}, nil
	}

	// Check expired
	if key.ExpiresAt != nil && key.ExpiresAt.AsTime().Before(time.Now()) {
		return &gen.ValidateAPIKeyResponse{Valid: false}, nil
	}

	// Build scopes list
	var scopes []string
	for _, p := range key.Scopes {
		scopes = append(scopes, fmt.Sprintf("%s:%s", p.Resource, p.Action))
	}

	claims := authentication.Claims
	if !claims.Member {
		// User-owned keys stop authenticating as soon as their owner leaves the
		// organization. A stale key can never outlive membership revocation.
		return &gen.ValidateAPIKeyResponse{Valid: false}, nil
	}
	claims.Roles = appendUnique(claims.Roles, claims.OrgRole)
	claims.Roles = appendUnique(claims.Roles, claims.PlatformRole)
	if claims.Attributes == nil {
		claims.Attributes = map[string]string{}
	}
	claims.Attributes["org_role"] = claims.OrgRole
	claims.Attributes["platform_role"] = claims.PlatformRole

	return &gen.ValidateAPIKeyResponse{
		Valid:          true,
		UserId:         key.UserId,
		OrganizationId: key.OrganizationId,
		Scopes:         scopes,
		Workspaces:     claims.Workspaces,
		Roles:          claims.Roles,
		Attributes:     claims.Attributes,
		// User-owned API keys authenticate the human principal; service/agent
		// principals authenticate via the token flow (delegation-minted), not
		// via user keys — so this is constant here, not a guess.
		PrincipalKind: "human",
	}, nil
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// ListAPIKeys returns non-revoked API keys for an org.
func (s *Service) ListAPIKeys(ctx context.Context, req *gen.ListAPIKeysRequest) (*gen.ListAPIKeysResponse, error) {
	var keys []*gen.APIKey
	var nextToken string
	err := s.store.WithOrgTx(ctx, req.OrganizationId, func(ctx context.Context) error {
		ks, nt, err := s.store.ListAPIKeys(ctx, req.OrganizationId, req.PageSize, req.PageToken)
		keys, nextToken = ks, nt
		return err
	})
	if err != nil {
		return nil, err
	}
	return &gen.ListAPIKeysResponse{Keys: keys, NextPageToken: nextToken}, nil
}

// RevokeAPIKey marks a key as revoked. Audit-logged with the key id so
// "which keys got revoked, by whom, when" is queryable from the audit
// trail.
//
// req only carries Id; we don't know the org. The handler gates on
// platform-admin (rpcs.go RevokeAPIKey requires `requirePlatformAdmin`),
// so the caller is privileged-by-policy. WithControlPlane lets the UPDATE
// hit the row regardless of its tenant.
//
// Phase 3 idea: thread orgID into the proto so the WithControlPlane step
// goes away and org-admins can revoke their own keys without
// platform-admin perms.
func (s *Service) RevokeAPIKey(ctx context.Context, actorID string, req *gen.RevokeAPIKeyRequest) error {
	// Org-scoped revoke: the store statement pins id AND organization_id, so an
	// org admin can never revoke another org's key by id (handler authorized
	// the actor for req.OrganizationId; the WHERE enforces the binding).
	if err := s.store.WithOrgTx(ctx, req.OrganizationId, func(ctx context.Context) error {
		return s.store.RevokeAPIKey(ctx, req.Id, req.OrganizationId)
	}); err != nil {
		return err
	}
	s.emit(ctx, actorID, "user", EventAPIKeyRevoked, "api_key", req.Id, req.OrganizationId)
	return nil
}

// randomBase62 returns n characters drawn uniformly from the base62
// alphabet. Random bytes at or above the largest multiple of 62 that fits
// in a byte are rejected rather than folded with a plain modulo, so the
// last four alphabet characters are not over-represented — a naive
// `b % 62` biases the keyspace and shrinks its effective entropy.
func randomBase62(n int) (string, error) {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const maxUnbiased = 256 - (256 % len(charset)) // 248
	out := make([]byte, n)
	buf := make([]byte, n)
	for filled := 0; filled < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= maxUnbiased {
				continue
			}
			out[filled] = charset[int(b)%len(charset)]
			filled++
			if filled == n {
				break
			}
		}
	}
	return string(out), nil
}
