package ed25519minter_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"api/pkg/auth"
	ed25519minter "api/pkg/auth/ed25519"
)

// memoryStore — duplicated from pkg/auth/memory_store_test.go because _test
// files don't export across packages. Keep in sync if that one changes.
type memoryStore struct {
	mu      sync.Mutex
	records []auth.SessionRecord
}

func (s *memoryStore) Insert(_ context.Context, rec *auth.SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, *rec)
	return nil
}

func (s *memoryStore) FindByRefreshHash(_ context.Context, hash []byte) (*auth.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if bytes.Equal(s.records[i].RefreshHash, hash) {
			r := s.records[i]
			return &r, nil
		}
	}
	return nil, auth.ErrRefreshRevoked
}

func (s *memoryStore) RevokeFamily(_ context.Context, familyID uuid.UUID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for i := range s.records {
		if s.records[i].FamilyID == familyID && s.records[i].RevokedAt == nil {
			t := now
			s.records[i].RevokedAt = &t
			s.records[i].RevokedReason = reason
		}
	}
	return nil
}

func (s *memoryStore) RevokeByUserID(_ context.Context, userID uuid.UUID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for i := range s.records {
		if s.records[i].UserID == userID && s.records[i].RevokedAt == nil {
			t := now
			s.records[i].RevokedAt = &t
			s.records[i].RevokedReason = reason
		}
	}
	return nil
}

func (s *memoryStore) countActive() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.records {
		if r.RevokedAt == nil {
			n++
		}
	}
	return n
}

func newMinter(t *testing.T) (*ed25519minter.Minter, *memoryStore) {
	t.Helper()
	_, priv, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	store := &memoryStore{}
	m := ed25519minter.New(ed25519minter.Config{
		Issuer:   "test-issuer",
		Audience: "test-audience",
	}, priv, store)
	return m, store
}

func newIdentity() *auth.Identity {
	return &auth.Identity{
		UserID:       uuid.Must(uuid.NewV7()),
		OrgID:        uuid.Must(uuid.NewV7()),
		OrgRole:      "admin",
		PlatformRole: "super_admin",
	}
}

func TestMint_And_VerifyAccess_Roundtrip(t *testing.T) {
	ctx := context.Background()
	m, _ := newMinter(t)
	want := newIdentity()

	pair, err := m.Mint(ctx, want)
	require.NoError(t, err)
	require.NotEmpty(t, pair.AccessToken)
	require.NotEmpty(t, pair.RefreshToken)

	got, err := m.VerifyAccess(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, want.UserID, got.UserID)
	require.Equal(t, want.OrgID, got.OrgID)
	require.Equal(t, want.OrgRole, got.OrgRole)
	require.Equal(t, want.PlatformRole, got.PlatformRole)
	require.NotEqual(t, uuid.Nil, got.SessionID, "session id must be set")
}

func TestVerifyAccess_AlgNoneRejected(t *testing.T) {
	// Construct an unsigned "alg: none" token for the same subject — a
	// classic CVE. Must be rejected at parse time.
	m, _ := newMinter(t)

	header := `{"alg":"none","typ":"JWT"}`
	payload := `{"iss":"test-issuer","aud":"test-audience","sub":"` +
		uuid.Must(uuid.NewV7()).String() + `","exp":9999999999,"sid":"` +
		uuid.Must(uuid.NewV7()).String() + `"}`
	enc := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	forged := enc(header) + "." + enc(payload) + "."

	_, err := m.VerifyAccess(forged)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrTokenAlgForbidden) ||
		errors.Is(err, auth.ErrTokenSignature) ||
		errors.Is(err, auth.ErrTokenMalformed),
		"expected alg/signature/malformed rejection, got %v", err)
}

func TestVerifyAccess_WrongSignature(t *testing.T) {
	ctx := context.Background()
	m, _ := newMinter(t)
	want := newIdentity()

	pair, err := m.Mint(ctx, want)
	require.NoError(t, err)

	// Flip a bit in the signature segment (last base64 chunk).
	parts := strings.Split(pair.AccessToken, ".")
	require.Len(t, parts, 3)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[0] ^= 0xff
	parts[2] = base64.RawURLEncoding.EncodeToString(sig)
	tampered := strings.Join(parts, ".")

	_, err = m.VerifyAccess(tampered)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrTokenSignature))
}

func TestVerifyAccess_WrongIssuer(t *testing.T) {
	ctx := context.Background()
	// Mint with one issuer
	_, priv, _ := ed25519minter.GenerateKey()
	store := &memoryStore{}
	m1 := ed25519minter.New(ed25519minter.Config{Issuer: "issuer-a", Audience: "aud"}, priv, store)
	pair, err := m1.Mint(ctx, newIdentity())
	require.NoError(t, err)

	// Verify with a different issuer (same key, different config)
	m2 := ed25519minter.New(ed25519minter.Config{Issuer: "issuer-b", Audience: "aud"}, priv, store)
	_, err = m2.VerifyAccess(pair.AccessToken)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrTokenWrongIssuer))
}

func TestVerifyAccess_WrongAudience(t *testing.T) {
	ctx := context.Background()
	_, priv, _ := ed25519minter.GenerateKey()
	store := &memoryStore{}
	m1 := ed25519minter.New(ed25519minter.Config{Issuer: "iss", Audience: "aud-a"}, priv, store)
	pair, err := m1.Mint(ctx, newIdentity())
	require.NoError(t, err)

	m2 := ed25519minter.New(ed25519minter.Config{Issuer: "iss", Audience: "aud-b"}, priv, store)
	_, err = m2.VerifyAccess(pair.AccessToken)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrTokenWrongAudience))
}

func TestVerifyAccess_Expired(t *testing.T) {
	ctx := context.Background()
	_, priv, _ := ed25519minter.GenerateKey()
	store := &memoryStore{}
	m := ed25519minter.New(ed25519minter.Config{
		Issuer:         "iss",
		Audience:       "aud",
		AccessTokenTTL: 1 * time.Nanosecond,
		ClockSkew:      1 * time.Nanosecond,
	}, priv, store)
	pair, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)
	_, err = m.VerifyAccess(pair.AccessToken)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrTokenExpired))
}

func TestRefresh_Rotates_And_InvalidatesPrevious(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	id := newIdentity()

	original, err := m.Mint(ctx, id)
	require.NoError(t, err)

	rotated, err := m.VerifyRefresh(ctx, original.RefreshToken)
	require.NoError(t, err)
	require.NotEqual(t, original.RefreshToken, rotated.RefreshToken,
		"rotation must issue a new refresh token")
	require.NotEqual(t, original.AccessToken, rotated.AccessToken,
		"rotation must issue a new access token")
	require.Equal(t, 1, store.countActive(), "exactly one active session after rotation")
}

func TestRefresh_ReuseDetected_KillsFamily(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	id := newIdentity()

	original, err := m.Mint(ctx, id)
	require.NoError(t, err)

	// Legitimate rotation
	rotated, err := m.VerifyRefresh(ctx, original.RefreshToken)
	require.NoError(t, err)

	// Attacker replays the original (now-revoked) refresh.
	_, err = m.VerifyRefresh(ctx, original.RefreshToken)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrRefreshReuse),
		"reuse of a rotated refresh must trigger reuse detection")

	// The new refresh should ALSO be dead, because reuse of the old one
	// killed the whole family.
	_, err = m.VerifyRefresh(ctx, rotated.RefreshToken)
	require.Error(t, err,
		"family revocation must invalidate all sessions in the family")

	require.Equal(t, 0, store.countActive(), "no active sessions after reuse")
}

func TestRefresh_UnknownTokenReturnsRevoked(t *testing.T) {
	ctx := context.Background()
	m, _ := newMinter(t)

	// Pass a well-formed but never-issued refresh token.
	_, err := m.VerifyRefresh(ctx, "never-existed")
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrRefreshRevoked),
		"unknown tokens must not reveal existence via distinct error")
}

func TestRevoke_Idempotent(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)

	original, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	require.NoError(t, m.Revoke(ctx, original.RefreshToken))
	require.Equal(t, 0, store.countActive())

	// Second revoke on the same token must succeed (no-op).
	require.NoError(t, m.Revoke(ctx, original.RefreshToken))
}

func TestMint_TokenContainsKid(t *testing.T) {
	ctx := context.Background()
	m, _ := newMinter(t)
	pair, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	// Decode the header to verify kid is set — sidecar uses this for
	// key rotation.
	header := strings.Split(pair.AccessToken, ".")[0]
	raw, err := base64.RawURLEncoding.DecodeString(header)
	require.NoError(t, err)

	var h map[string]any
	require.NoError(t, json.Unmarshal(raw, &h))
	require.Equal(t, "EdDSA", h["alg"])
	require.Equal(t, m.KeyID(), h["kid"])
}

func TestKeyID_IsDeterministic(t *testing.T) {
	// Same key → same kid, across instantiations.
	_, priv, _ := ed25519minter.GenerateKey()
	m1 := ed25519minter.New(ed25519minter.Config{}, priv, &memoryStore{})
	m2 := ed25519minter.New(ed25519minter.Config{}, priv, &memoryStore{})
	require.Equal(t, m1.KeyID(), m2.KeyID())

	_, priv2, _ := ed25519minter.GenerateKey()
	m3 := ed25519minter.New(ed25519minter.Config{}, priv2, &memoryStore{})
	require.NotEqual(t, m1.KeyID(), m3.KeyID(), "different keys must yield different kids")
}

// Compile-time assertion that our test store satisfies SessionStore.
var _ auth.SessionStore = (*memoryStore)(nil)

// Compile-time use of ed25519 package to prevent accidental removal.
var _ = ed25519.Sign
