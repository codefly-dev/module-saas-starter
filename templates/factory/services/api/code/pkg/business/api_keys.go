package business

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"

	"backend/pkg/gen"
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

	keyID := uuid.New().String()
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

	if err := s.store.CreateAPIKey(ctx, key, keyHash); err != nil {
		return nil, w.Wrapf(err, "cannot store API key")
	}

	s.emit(ctx, userID, "user", "api_key.created", "api_key", keyID, req.OrganizationId)

	return &gen.CreateAPIKeyResponse{
		Key:          key,
		PlaintextKey: plaintext,
	}, nil
}

// ValidateAPIKey checks a hashed key against the store.
func (s *Service) ValidateAPIKey(ctx context.Context, plaintextKey string) (*gen.ValidateAPIKeyResponse, error) {
	w := wool.Get(ctx).In("ValidateAPIKey")

	if s.hasher == nil {
		return nil, w.NewError("key hasher not configured")
	}

	keyHash, err := s.hasher.HashKey(ctx, plaintextKey)
	if err != nil {
		return nil, w.Wrapf(err, "cannot hash key")
	}

	key, err := s.store.GetAPIKeyByHash(ctx, keyHash)
	if err != nil {
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

	return &gen.ValidateAPIKeyResponse{
		Valid:          true,
		UserId:         key.UserId,
		OrganizationId: key.OrganizationId,
		Scopes:         scopes,
	}, nil
}

// ListAPIKeys returns non-revoked API keys for an org.
func (s *Service) ListAPIKeys(ctx context.Context, req *gen.ListAPIKeysRequest) (*gen.ListAPIKeysResponse, error) {
	keys, nextToken, err := s.store.ListAPIKeys(ctx, req.OrganizationId, req.PageSize, req.PageToken)
	if err != nil {
		return nil, err
	}
	return &gen.ListAPIKeysResponse{Keys: keys, NextPageToken: nextToken}, nil
}

// RevokeAPIKey marks a key as revoked.
func (s *Service) RevokeAPIKey(ctx context.Context, req *gen.RevokeAPIKeyRequest) error {
	return s.store.RevokeAPIKey(ctx, req.Id)
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
