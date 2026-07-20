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

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
)

// memoryStore — duplicated from pkg/auth/memory_store_test.go because _test
// files don't export across packages. Keep in sync if that one changes.
type memoryStore struct {
	mu                   sync.Mutex
	records              []auth.SessionRecord
	rotationFailure      error
	refreshAuthorization *auth.RefreshAuthorization
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

func (s *memoryStore) RotateRefresh(
	_ context.Context,
	hash []byte,
	replacement func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) (*auth.SessionRecord, error),
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if !bytes.Equal(s.records[i].RefreshHash, hash) {
			continue
		}
		if s.records[i].RevokedAt != nil {
			if s.records[i].RevokedReason != "rotated" {
				return auth.ErrRefreshRevoked
			}
			now := time.Now()
			for j := range s.records {
				if s.records[j].UserID == s.records[i].UserID && s.records[j].RevokedAt == nil {
					t := now
					s.records[j].RevokedAt = &t
					s.records[j].RevokedReason = "refresh-reuse-all-sessions"
				}
			}
			return auth.ErrRefreshReuse
		}

		current := s.records[i]
		authorization := auth.RefreshAuthorization{
			OrgID:        current.OrgID,
			OrgRole:      current.OrgRole,
			PlatformRole: current.PlatformRole,
			MFAEnrolled: auth.Assurance{
				AuthenticationMethods: current.AuthenticationMethods,
				Level:                 current.AssuranceLevel,
				MFAVerifiedAt:         current.MFAVerifiedAt,
			}.HasMFAEvidence(),
		}
		if s.refreshAuthorization != nil {
			authorization = *s.refreshAuthorization
		}
		next, err := replacement(&current, authorization)
		if err != nil {
			if reason, terminal := auth.RefreshRejectionReason(err); terminal {
				now := time.Now()
				for j := range s.records {
					if s.records[j].FamilyID == current.FamilyID && s.records[j].RevokedAt == nil {
						t := now
						s.records[j].RevokedAt = &t
						s.records[j].RevokedReason = reason
					}
				}
				return auth.ErrRefreshRevoked
			}
			return err
		}
		if next == nil || next.UserID != current.UserID || next.FamilyID != current.FamilyID ||
			next.ID == current.ID || bytes.Equal(next.RefreshHash, current.RefreshHash) {
			return errors.New("memory session store: invalid refresh replacement")
		}
		if s.rotationFailure != nil {
			return s.rotationFailure
		}
		now := time.Now()
		for j := range s.records {
			if s.records[j].FamilyID == current.FamilyID && s.records[j].RevokedAt == nil {
				t := now
				s.records[j].RevokedAt = &t
				s.records[j].RevokedReason = "rotated"
			}
		}
		s.records = append(s.records, *next)
		return nil
	}
	return auth.ErrRefreshRevoked
}

func (s *memoryStore) ExchangeOrganization(
	_ context.Context,
	userID uuid.UUID,
	sessionID uuid.UUID,
	targetOrgID uuid.UUID,
	issue func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		current := &s.records[i]
		if current.ID != sessionID || current.UserID != userID || current.RevokedAt != nil {
			continue
		}
		authorization := auth.RefreshAuthorization{
			OrgID:        targetOrgID,
			OrgRole:      current.OrgRole,
			PlatformRole: current.PlatformRole,
			MFAEnrolled: auth.Assurance{
				AuthenticationMethods: current.AuthenticationMethods,
				Level:                 current.AssuranceLevel,
				MFAVerifiedAt:         current.MFAVerifiedAt,
			}.HasMFAEvidence(),
		}
		if s.refreshAuthorization != nil {
			authorization = *s.refreshAuthorization
			authorization.OrgID = targetOrgID
		}
		if err := issue(current, authorization); err != nil {
			return err
		}
		current.OrgID = authorization.OrgID
		current.OrgRole = authorization.OrgRole
		current.PlatformRole = authorization.PlatformRole
		return nil
	}
	return auth.ErrSessionUnavailable
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
	want.AuthenticationMethods = []string{auth.AuthenticationMethodOAuth, auth.AuthenticationMethodOTP}
	want.AuthenticatedAt = time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	want.AssuranceLevel = auth.AssuranceLevelAAL2
	want.MFAVerifiedAt = time.Now().Add(-time.Minute).Truncate(time.Second)
	want.MFASatisfied = true

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
	require.Equal(t, want.AuthenticationMethods, got.AuthenticationMethods)
	require.Equal(t, want.AuthenticatedAt, got.AuthenticatedAt)
	require.Equal(t, want.AssuranceLevel, got.AssuranceLevel)
	require.Equal(t, want.MFAVerifiedAt, got.MFAVerifiedAt)
	require.True(t, got.MFASatisfied)
	require.NotEqual(t, uuid.Nil, got.SessionID, "session id must be set")
}

func TestRefreshPreservesAuthenticationEvidenceWithoutRenewingStepUp(t *testing.T) {
	ctx := context.Background()
	m, _ := newMinter(t)
	want := newIdentity()
	want.AuthenticationMethods = []string{auth.AuthenticationMethodEmail, auth.AuthenticationMethodRecovery}
	want.AuthenticatedAt = time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	want.AssuranceLevel = auth.AssuranceLevelAAL2
	want.MFAVerifiedAt = time.Now().Add(-9 * time.Minute).Truncate(time.Second)
	want.MFASatisfied = true

	original, err := m.Mint(ctx, want)
	require.NoError(t, err)
	rotated, err := m.VerifyRefresh(ctx, original.RefreshToken)
	require.NoError(t, err)
	got, err := m.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, want.AuthenticationMethods, got.AuthenticationMethods)
	require.Equal(t, want.AuthenticatedAt, got.AuthenticatedAt)
	require.Equal(t, want.AssuranceLevel, got.AssuranceLevel)
	require.Equal(t, want.MFAVerifiedAt, got.MFAVerifiedAt)
}

func TestRefreshProjectsCurrentOrganizationAndPlatformRoles(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	originalIdentity := newIdentity()
	originalIdentity.MFASatisfied = true
	originalIdentity.AuthenticationMethods = []string{auth.AuthenticationMethodOAuth}
	originalIdentity.AuthenticatedAt = time.Now().Add(-time.Hour).Truncate(time.Second)
	originalIdentity.AssuranceLevel = auth.AssuranceLevelAAL1

	original, err := m.Mint(ctx, originalIdentity)
	require.NoError(t, err)

	currentOrgID := uuid.Must(uuid.NewV7())
	store.refreshAuthorization = &auth.RefreshAuthorization{
		OrgID:        currentOrgID,
		OrgRole:      "member",
		PlatformRole: "support",
		MFAEnrolled:  false,
	}
	rotated, err := m.VerifyRefresh(ctx, original.RefreshToken)
	require.NoError(t, err)

	got, err := m.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, currentOrgID, got.OrgID)
	require.Equal(t, "member", got.OrgRole)
	require.Equal(t, "support", got.PlatformRole)
}

func TestSwitchOrganizationPreservesDeviceSessionAndRefreshCredential(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	identity := newIdentity()
	identity.MFASatisfied = true
	identity.AuthenticationMethods = []string{auth.AuthenticationMethodOAuth}
	identity.AuthenticatedAt = time.Now().Add(-time.Hour).Truncate(time.Second)
	identity.AssuranceLevel = auth.AssuranceLevelAAL1
	identity.DeviceInfo = map[string]string{"description": "Firefox on Linux"}
	identity.IPAddress = "203.0.113.9"

	pair, err := m.Mint(ctx, identity)
	require.NoError(t, err)
	store.mu.Lock()
	before := store.records[0]
	store.mu.Unlock()

	targetOrgID := uuid.Must(uuid.NewV7())
	store.refreshAuthorization = &auth.RefreshAuthorization{
		OrgID:        targetOrgID,
		OrgRole:      "member",
		PlatformRole: "support",
		MFAEnrolled:  false,
	}
	accessToken, err := m.SwitchOrganization(ctx, identity.UserID, before.ID, targetOrgID)
	require.NoError(t, err)

	switched, err := m.VerifyAccess(accessToken)
	require.NoError(t, err)
	require.Equal(t, targetOrgID, switched.OrgID)
	require.Equal(t, "member", switched.OrgRole)
	require.Equal(t, "support", switched.PlatformRole)
	require.Equal(t, before.ID, switched.SessionID)

	store.mu.Lock()
	require.Len(t, store.records, 1, "switching must not create another device session")
	after := store.records[0]
	store.mu.Unlock()
	require.Equal(t, before.FamilyID, after.FamilyID)
	require.Equal(t, before.RefreshHash, after.RefreshHash)
	require.Equal(t, before.IssuedAt, after.IssuedAt)
	require.Equal(t, before.LastActiveAt, after.LastActiveAt)
	require.Equal(t, before.IdleExpiresAt, after.IdleExpiresAt)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
	require.Equal(t, before.DeviceInfo, after.DeviceInfo)
	require.Equal(t, before.IPAddress, after.IPAddress)
	require.Nil(t, after.RevokedAt)

	rotated, err := m.VerifyRefresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "the pre-switch refresh credential must remain valid")
	refreshed, err := m.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, targetOrgID, refreshed.OrgID)
	require.Equal(t, "member", refreshed.OrgRole)
}

func TestRefreshRequiresReauthenticationWhenMFAWasNewlyEnrolled(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	identity := newIdentity()
	identity.MFASatisfied = true
	identity.AuthenticationMethods = []string{auth.AuthenticationMethodEmail}
	identity.AuthenticatedAt = time.Now().Add(-time.Hour).Truncate(time.Second)
	identity.AssuranceLevel = auth.AssuranceLevelAAL1

	original, err := m.Mint(ctx, identity)
	require.NoError(t, err)
	store.refreshAuthorization = &auth.RefreshAuthorization{
		OrgID:        identity.OrgID,
		OrgRole:      identity.OrgRole,
		PlatformRole: identity.PlatformRole,
		MFAEnrolled:  true,
	}

	_, err = m.VerifyRefresh(ctx, original.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)
	require.Equal(t, 0, store.countActive(),
		"a session without second-factor evidence must be terminal once MFA is enrolled")
}

func TestRefreshDowngradesAuthenticationEvidenceWhenMFAWasRemoved(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	identity := newIdentity()
	identity.MFASatisfied = true
	identity.AuthenticationMethods = []string{auth.AuthenticationMethodOAuth, auth.AuthenticationMethodWebAuthn}
	identity.AuthenticatedAt = time.Now().Add(-time.Hour).Truncate(time.Second)
	identity.AssuranceLevel = auth.AssuranceLevelAAL2
	identity.MFAVerifiedAt = time.Now().Add(-30 * time.Minute).Truncate(time.Second)

	original, err := m.Mint(ctx, identity)
	require.NoError(t, err)
	store.refreshAuthorization = &auth.RefreshAuthorization{
		OrgID:        identity.OrgID,
		OrgRole:      identity.OrgRole,
		PlatformRole: identity.PlatformRole,
		MFAEnrolled:  false,
	}

	rotated, err := m.VerifyRefresh(ctx, original.RefreshToken)
	require.NoError(t, err)
	got, err := m.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AssuranceLevelAAL1, got.AssuranceLevel)
	require.Equal(t, []string{auth.AuthenticationMethodOAuth}, got.AuthenticationMethods)
	require.True(t, got.MFAVerifiedAt.IsZero())
	require.True(t, got.MFASatisfied)
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

func TestRefresh_ReuseDetected_KillsAllUserSessions(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	id := newIdentity()

	original, err := m.Mint(ctx, id)
	require.NoError(t, err)
	bystander, err := m.Mint(ctx, id)
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
	_, err = m.VerifyRefresh(ctx, bystander.RefreshToken)
	require.Error(t, err, "replay must invalidate the user's other session families")

	require.Equal(t, 0, store.countActive(), "no active sessions after reuse")
}

func TestRefresh_AdministrativeRevocationIsNotClassifiedAsReplay(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	identity := newIdentity()
	original, err := m.Mint(ctx, identity)
	require.NoError(t, err)
	bystander, err := m.Mint(ctx, identity)
	require.NoError(t, err)

	require.NoError(t, m.Revoke(ctx, original.RefreshToken))
	_, err = m.VerifyRefresh(ctx, original.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)
	require.NotErrorIs(t, err, auth.ErrRefreshReuse)
	require.Equal(t, 1, store.countActive(),
		"presenting an administratively revoked token must not kill unrelated sessions")

	_, err = m.VerifyRefresh(ctx, bystander.RefreshToken)
	require.NoError(t, err)
}

func TestRefresh_ConcurrentConsumptionHasOneWinnerAndCommitsReplayRevocation(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	original, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	type result struct {
		pair *auth.TokenPair
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			pair, err := m.VerifyRefresh(ctx, original.RefreshToken)
			results <- result{pair: pair, err: err}
		}()
	}
	close(start)

	var winner *auth.TokenPair
	var successCount, reuseCount int
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			successCount++
			winner = got.pair
		case errors.Is(got.err, auth.ErrRefreshReuse):
			reuseCount++
		default:
			t.Fatalf("unexpected concurrent refresh result: %v", got.err)
		}
	}
	require.Equal(t, 1, successCount)
	require.Equal(t, 1, reuseCount)
	require.NotNil(t, winner)
	require.Equal(t, 0, store.countActive(),
		"the losing replay must commit revocation of the winning successor")

	_, err = m.VerifyRefresh(ctx, winner.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)
	require.NotErrorIs(t, err, auth.ErrRefreshReuse,
		"a successor revoked by replay response is not itself a consumed token")
}

func TestRefresh_ReplacementFailureDoesNotConsumePresentedToken(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	original, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	injected := errors.New("injected successor insert failure")
	store.rotationFailure = injected
	_, err = m.VerifyRefresh(ctx, original.RefreshToken)
	require.ErrorIs(t, err, injected)
	require.Equal(t, 1, store.countActive(),
		"a failed successor insert must leave the presented token active")

	store.rotationFailure = nil
	rotated, err := m.VerifyRefresh(ctx, original.RefreshToken)
	require.NoError(t, err)
	require.NotEmpty(t, rotated.RefreshToken)
	require.Equal(t, 1, store.countActive())
}

func TestRefreshPreservesAbsoluteLifetimeAndDeviceContext(t *testing.T) {
	ctx := context.Background()
	_, priv, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	store := &memoryStore{}
	m := ed25519minter.New(ed25519minter.Config{
		SessionPolicy: auth.SessionPolicy{
			AbsoluteLifetime: time.Hour,
			IdleTimeout:      30 * time.Minute,
			MaxActiveDevices: 2,
		},
	}, priv, store)
	identity := newIdentity()
	identity.DeviceInfo = map[string]string{"description": "Firefox on Linux"}
	identity.IPAddress = "203.0.113.7"
	original, err := m.Mint(ctx, identity)
	require.NoError(t, err)

	store.mu.Lock()
	first := store.records[0]
	store.mu.Unlock()
	rotated, err := m.VerifyRefresh(ctx, original.RefreshToken)
	require.NoError(t, err)
	require.NotEmpty(t, rotated.RefreshToken)

	store.mu.Lock()
	require.Len(t, store.records, 2)
	next := store.records[1]
	store.mu.Unlock()
	require.Equal(t, first.IssuedAt, next.IssuedAt)
	require.Equal(t, first.ExpiresAt, next.ExpiresAt)
	require.True(t, next.LastActiveAt.After(first.LastActiveAt) || next.LastActiveAt.Equal(first.LastActiveAt))
	require.Equal(t, map[string]string{"description": "Firefox on Linux"}, next.DeviceInfo)
	require.Equal(t, "203.0.113.7", next.IPAddress)
	require.True(t, next.IdleExpiresAt.After(next.LastActiveAt))
	require.False(t, next.IdleExpiresAt.After(next.ExpiresAt))
}

func TestRefreshRejectsAbsoluteAndIdleExpiryWithDistinctTerminalReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		expire func(*auth.SessionRecord)
	}{
		{
			name:   "absolute",
			reason: auth.RefreshRejectionAbsoluteLifetime,
			expire: func(rec *auth.SessionRecord) { rec.ExpiresAt = time.Now().Add(-time.Minute) },
		},
		{
			name:   "idle",
			reason: auth.RefreshRejectionIdleTimeout,
			expire: func(rec *auth.SessionRecord) { rec.IdleExpiresAt = time.Now().Add(-time.Minute) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, store := newMinter(t)
			pair, err := m.Mint(ctx, newIdentity())
			require.NoError(t, err)
			store.mu.Lock()
			tc.expire(&store.records[0])
			store.mu.Unlock()

			_, err = m.VerifyRefresh(ctx, pair.RefreshToken)
			require.ErrorIs(t, err, auth.ErrRefreshRevoked)
			store.mu.Lock()
			require.Equal(t, tc.reason, store.records[0].RevokedReason)
			store.mu.Unlock()
		})
	}
}

func TestMintRejectsInvalidSessionPolicy(t *testing.T) {
	_, priv, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	m := ed25519minter.New(ed25519minter.Config{
		SessionPolicy: auth.SessionPolicy{
			AbsoluteLifetime: time.Hour,
			IdleTimeout:      2 * time.Hour,
			MaxActiveDevices: 1,
		},
	}, priv, &memoryStore{})
	_, err = m.Mint(context.Background(), newIdentity())
	require.ErrorContains(t, err, "invalid session policy")
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
