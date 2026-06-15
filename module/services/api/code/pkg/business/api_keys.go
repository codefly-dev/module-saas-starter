package business

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"

	"api/pkg/gen"
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

	// Check API key quota
	if s.entitlements != nil {
		ok, err := s.entitlements.CheckQuota(ctx, req.OrganizationId, "api_keys")
		if err != nil {
			return nil, w.Wrapf(err, "cannot check API key quota")
		}
		if !ok {
			return nil, w.NewError("API key limit reached for your plan")
		}
	}

	// Generate random key material (32 bytes = 256 bits)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, w.Wrapf(err, "cannot generate random key")
	}

	// Format: cfly_sk_{env}_{base62}
	envPrefix := "live"
	if req.Environment == gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST {
		envPrefix = "test"
	}
	encoded := base62Encode(raw)
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
		return s.store.CreateAPIKey(ctx, key, keyHash)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot store API key")
	}

	s.emit(ctx, userID, "user", "api_key.created", "api_key", keyID, req.OrganizationId)

	return &gen.CreateAPIKeyResponse{
		Key:          key,
		PlaintextKey: plaintext,
	}, nil
}

// ValidateAPIKey checks a hashed key against the store.
//
// Cross-tenant lookup: the plaintext key is presented by an
// unauthenticated request — we don't yet know which tenant it
// belongs to. WithBypass elevates so the GetAPIKeyByHash scan can
// match across all orgs. The returned key carries OrganizationId
// which the auth interceptor stamps on the request context, so
// every subsequent op runs as the right tenant.
func (s *Service) ValidateAPIKey(ctx context.Context, plaintextKey string) (*gen.ValidateAPIKeyResponse, error) {
	w := wool.Get(ctx).In("ValidateAPIKey")

	if s.hasher == nil {
		return nil, w.NewError("key hasher not configured")
	}

	keyHash, err := s.hasher.HashKey(ctx, plaintextKey)
	if err != nil {
		return nil, w.Wrapf(err, "cannot hash key")
	}

	var key *gen.APIKey
	if err := s.store.WithBypass(ctx, func(ctx context.Context) error {
		k, err := s.store.GetAPIKeyByHash(ctx, keyHash)
		key = k
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot look up key")
	}
	if key == nil {
		return &gen.ValidateAPIKeyResponse{Valid: false}, nil
	}

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

	// Identity Claims v1 — a policy-enforcement consumer (e.g. an AI gateway)
	// builds the caller's full execution context from THIS one response: team
	// paths (workspaces), RBAC role names, profile attributes. Claims read runs
	// under the same bypass rationale as the key lookup (pre-auth, cross-tenant);
	// a claims failure fails the validate (a half-built identity is worse than
	// a retry).
	var workspaces, roles []string
	var attributes map[string]string
	if err := s.store.WithBypass(ctx, func(ctx context.Context) error {
		var err error
		if workspaces, err = s.store.ListTeamPathsForUser(ctx, key.UserId, key.OrganizationId); err != nil {
			return err
		}
		if roles, err = s.store.ListRoleNamesForUser(ctx, key.UserId, key.OrganizationId); err != nil {
			return err
		}
		attributes, err = s.store.GetUserAttributes(ctx, key.UserId)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot load identity claims")
	}

	return &gen.ValidateAPIKeyResponse{
		Valid:          true,
		UserId:         key.UserId,
		OrganizationId: key.OrganizationId,
		Scopes:         scopes,
		Workspaces:     workspaces,
		Roles:          roles,
		Attributes:     attributes,
		// User-owned API keys authenticate the human principal; service/agent
		// principals authenticate via the token flow (delegation-minted), not
		// via user keys — so this is constant here, not a guess.
		PrincipalKind: "human",
	}, nil
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
// so the caller is privileged-by-policy. WithBypass lets the UPDATE
// hit the row regardless of its tenant.
//
// Phase 3 idea: thread orgID into the proto so the WithBypass step
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
	s.emit(ctx, actorID, "user", "api_key.revoked", "api_key", req.Id, req.OrganizationId)
	return nil
}

// base62Encode encodes bytes to a base62 string (alphanumeric).
func base62Encode(data []byte) string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	encoded := make([]byte, len(data))
	for i, b := range data {
		encoded[i] = charset[int(b)%len(charset)]
	}
	return string(encoded)
}
