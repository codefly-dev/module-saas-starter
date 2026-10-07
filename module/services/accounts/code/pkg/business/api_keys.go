package business

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	for _, permission := range req.Scopes {
		if err := CheckScopeShape(permission); err != nil {
			return nil, err
		}
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
		if err := s.store.CreateAPIKey(ctx, key, keyHash); err != nil {
			return err
		}
		return s.emitTx(ctx, userID, "user", EventAPIKeyCreated, "api_key", keyID, req.OrganizationId)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot store API key")
	}

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

	// Build scopes list. A stored scope that cannot travel as one scope refuses
	// the whole key rather than being dropped: a row written before CreateAPIKey
	// checked the shape would otherwise be re-read downstream as a wider scope,
	// and dropping it would silently change what the key does.
	var scopes []string
	for _, p := range key.Scopes {
		if err := CheckScopeShape(p); err != nil {
			w.Warn("API key refused: a stored scope cannot travel as one scope", wool.Field("key_id", key.Id))
			return &gen.ValidateAPIKeyResponse{Valid: false}, nil
		}
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

// CheckScopeShape refuses a permission that cannot travel as one scope. A
// key's scopes cross every hop as strings: `resource:action`, comma-joined by
// the auth-gateway into X-Scopes and split again by each service. A `:` or `,`
// inside either half therefore reads back as a different scope, or as several —
// users / read,*:* passes the mint ceiling as a scope under users:*, then is
// re-read downstream as users:read and the root *:*. Whitespace and control
// characters are refused with them: the header parser trims the one and
// neither belongs in a name. Both halves must be present.
func CheckScopeShape(permission *gen.Permission) error {
	for _, half := range [...]struct{ name, value string }{
		{"resource", permission.GetResource()},
		{"action", permission.GetAction()},
	} {
		if half.value == "" {
			return status.Errorf(codes.InvalidArgument, "a scope's %s is empty", half.name)
		}
		if strings.IndexFunc(half.value, scopeSeparatorOrSpace) >= 0 {
			return status.Errorf(codes.InvalidArgument, "scope %s %q holds a ':', ',', whitespace or control character", half.name, half.value)
		}
	}
	return nil
}

func scopeSeparatorOrSpace(r rune) bool {
	return r == ':' || r == ',' || unicode.IsSpace(r) || unicode.IsControl(r)
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
// It is a NARROWING — a credential that authenticates stops authenticating —
// so it runs under the policy log: the entry is appended to the external record
// and receipted BEFORE the key is marked, and the revocation commits in the
// same transaction as the receipt's commit. A host that cannot witness the
// append refuses rather than revoking unwitnessed: a key revocation a restore
// could silently undo is a live credential nobody knows is live.
func (s *Service) RevokeAPIKey(ctx context.Context, actorID string, req *gen.RevokeAPIKeyRequest) error {
	// Org-scoped revoke: the store statement pins id AND organization_id, so an
	// org admin can never revoke another org's key by id (handler authorized
	// the actor for req.OrganizationId; the WHERE enforces the binding). That
	// WHERE is also what keeps the revoke confined now the transaction is the
	// policy log's control-plane one — the receipt relation is control-plane
	// only, and the receipt and the revocation have to be the same transaction.
	if err := s.WithPolicyLoggedNarrowing(ctx,
		revokeAPIKeyPolicyLogEntry(actorID, req.OrganizationId, req.Id),
		func(ctx context.Context) error {
			if err := s.store.RevokeAPIKey(ctx, req.Id, req.OrganizationId); err != nil {
				return err
			}
			return s.emitTx(ctx, actorID, "user", EventAPIKeyRevoked, "api_key", req.Id, req.OrganizationId)
		}); err != nil {
		return err
	}
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
