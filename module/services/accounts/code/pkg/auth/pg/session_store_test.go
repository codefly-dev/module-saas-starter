package pgauth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/wool"

	"accounts/internal/testdb"
	"accounts/pkg/auth"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/infra"
)

// testStore is the RLS-aware *PostgresStore (BeforeAcquire SET ROLE
// app_tenant on every connection). NewSessionStore is constructed
// against this so the per-method WithUserTx / WithControlPlane wraps
// exercise the production RLS path.
//
// testPool is a raw *pgxpool.Pool for places we need direct DB
// access (e.g. seedUser inserting a users row, which is in the
// RLS skip-list anyway).
var (
	testStore *infra.PostgresStore
	testPool  *pgxpool.Pool
)

// TestMain boots a real postgres (+ vault) via codefly WithDependencies and
// holds a pgxpool for the whole package. Matches the pattern in
// pkg/business/service_test.go so the two suites can run back-to-back.
func TestMain(m *testing.M) {
	os.Exit(runSessionStoreTests(m))
}

func runSessionStoreTests(m *testing.M) int {
	ctx := context.Background()
	wool.SetGlobalLogLevel(wool.DEBUG)

	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		sdk.WithNamingScope("pgauth-test"),
		sdk.WithTimeout(120*time.Second),
		sdk.WithSilence("store"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WithDependencies failed: %v\n", err)
		return 1
	}
	defer deps.Destroy(ctx)

	if _, err := codefly.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init failed: %v\n", err)
		return 1
	}

	conn, err := codefly.For(ctx).Service("store").Secret("postgres", "read-write-connection")
	if err != nil {
		fmt.Fprintf(os.Stderr, "get connection string: %v\n", err)
		return 1
	}

	store, err := infra.NewPostgresStoreFromURL(ctx, conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewPostgresStoreFromURL: %v\n", err)
		return 1
	}
	defer store.Close()
	testStore = store
	testPool = store.Pool()
	releasePackageLock, err := testdb.AcquirePackageLock(ctx, testPool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration test lock: %v\n", err)
		return 1
	}
	defer func() {
		if err := releasePackageLock(); err != nil {
			fmt.Fprintf(os.Stderr, "release integration test lock: %v\n", err)
		}
	}()

	return m.Run()
}

// seedUser inserts a minimum users row so sessions FK is happy.
func seedUser(t *testing.T) uuid.UUID {
	t.Helper()
	id := business.NewID()
	// users is RLS-protected; seed under WithControlPlane (elevates the tx).
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			INSERT INTO users (uuid, primary_email, status)
			VALUES ($1, $2, 'active')`,
			id, fmt.Sprintf("user-%s@test.local", id.String()))
		return err
	}))
	return id
}

func seedOrganizationMembership(t *testing.T, userID uuid.UUID, role string, joinedAt time.Time) uuid.UUID {
	t.Helper()
	orgID := business.NewID()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		if _, err := tx.Exec(ctx, `
			INSERT INTO organizations (id, name, slug, owner_id)
			VALUES ($1, $2, $3, $4)`,
			orgID, "Refresh Test Organization", fmt.Sprintf("refresh-%s", orgID), userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO organization_members (org_id, user_id, role, joined_at)
			VALUES ($1, $2, $3, $4)`, orgID, userID, role, joinedAt)
		return err
	}))
	return orgID
}

// scanControlPlane runs a single-row verification query under WithControlPlane — for tests
// asserting that an RLS-protected row exists (an un-contexted testPool read
// would be hidden by RLS and fail-close to zero).
func scanControlPlane(t *testing.T, dst any, query string, args ...any) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx, query, args...).Scan(dst)
	}))
}

func newRecord(userID uuid.UUID) *auth.SessionRecord {
	hash := sha256.Sum256([]byte(uuid.Must(uuid.NewV7()).String()))
	now := time.Now().Truncate(time.Microsecond)
	return &auth.SessionRecord{
		ID:            business.NewID(),
		UserID:        userID,
		FamilyID:      business.NewID(),
		RefreshHash:   hash[:],
		IssuedAt:      now,
		LastActiveAt:  now,
		IdleExpiresAt: now.Add(24 * time.Hour),
		ExpiresAt:     now.Add(7 * 24 * time.Hour),
		OrgRole:       "admin",
		PlatformRole:  "super_admin",
	}
}

func replacementRecord(current *auth.SessionRecord, authorization auth.RefreshAuthorization) *auth.SessionRecord {
	rec := newRecord(current.UserID)
	rec.FamilyID = current.FamilyID
	rec.OrgID = authorization.OrgID
	rec.OrgRole = authorization.OrgRole
	rec.PlatformRole = authorization.PlatformRole
	rec.MFASatisfied = current.MFASatisfied
	rec.AuthenticationMethods = append([]string(nil), current.AuthenticationMethods...)
	rec.AuthenticatedAt = current.AuthenticatedAt
	rec.AssuranceLevel = current.AssuranceLevel
	rec.MFAVerifiedAt = current.MFAVerifiedAt
	rec.DeviceInfo = maps.Clone(current.DeviceInfo)
	rec.IPAddress = current.IPAddress
	rec.IssuedAt = current.IssuedAt
	rec.LastActiveAt = time.Now()
	rec.IdleExpiresAt = rec.LastActiveAt.Add(24 * time.Hour)
	if rec.IdleExpiresAt.After(current.ExpiresAt) {
		rec.IdleExpiresAt = current.ExpiresAt
	}
	rec.ExpiresAt = current.ExpiresAt
	return rec
}

func TestSessionStore_InsertAndFind(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)

	userID := seedUser(t)
	rec := newRecord(userID)
	rec.AuthenticationMethods = []string{auth.AuthenticationMethodOAuth, auth.AuthenticationMethodOTP}
	rec.AuthenticatedAt = time.Now().Add(-2 * time.Minute).Truncate(time.Microsecond)
	rec.AssuranceLevel = auth.AssuranceLevelAAL2
	rec.MFAVerifiedAt = time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	rec.MFASatisfied = true
	rec.DeviceInfo = map[string]string{"description": "Safari on macOS"}
	rec.IPAddress = "203.0.113.12"

	require.NoError(t, store.Insert(ctx, rec))

	found, err := store.FindByRefreshHash(ctx, rec.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, rec.ID, found.ID)
	require.Equal(t, rec.UserID, found.UserID)
	require.Equal(t, rec.FamilyID, found.FamilyID)
	require.Equal(t, rec.OrgRole, found.OrgRole)
	require.Equal(t, rec.PlatformRole, found.PlatformRole)
	require.Equal(t, rec.AuthenticationMethods, found.AuthenticationMethods)
	require.Equal(t, rec.AuthenticatedAt, found.AuthenticatedAt)
	require.Equal(t, rec.AssuranceLevel, found.AssuranceLevel)
	require.Equal(t, rec.MFAVerifiedAt, found.MFAVerifiedAt)
	require.Equal(t, rec.DeviceInfo, found.DeviceInfo)
	require.Equal(t, rec.IPAddress, found.IPAddress)
	require.Equal(t, rec.IssuedAt, found.IssuedAt)
	require.Equal(t, rec.LastActiveAt, found.LastActiveAt)
	require.Equal(t, rec.IdleExpiresAt, found.IdleExpiresAt)
	require.Equal(t, rec.ExpiresAt, found.ExpiresAt)
	require.True(t, found.MFASatisfied)
	require.Nil(t, found.RevokedAt)
}

func TestSessionStore_FindByRefreshHash_NotFound(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)

	hash := sha256.Sum256([]byte("never-existed"))
	_, err := store.FindByRefreshHash(ctx, hash[:])
	require.ErrorIs(t, err, auth.ErrRefreshRevoked,
		"unknown hash must return ErrRefreshRevoked for oracle resistance")
}

func TestSessionStore_RevokeFamily(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)

	family := business.NewID()

	rec1 := newRecord(userID)
	rec1.FamilyID = family
	require.NoError(t, store.Insert(ctx, rec1))

	rec2 := newRecord(userID)
	rec2.FamilyID = family
	require.NoError(t, store.Insert(ctx, rec2))

	require.NoError(t, store.RevokeFamily(ctx, family, "logout"))
	// Both should now be revoked
	f1, err := store.FindByRefreshHash(ctx, rec1.RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, f1.RevokedAt)
	require.Equal(t, "logout", f1.RevokedReason)

	f2, err := store.FindByRefreshHash(ctx, rec2.RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, f2.RevokedAt)
}

func TestSessionStore_RevokeFamily_OnlyAffectsMatchingFamily(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)

	target := newRecord(userID)
	require.NoError(t, store.Insert(ctx, target))

	bystander := newRecord(userID)
	bystanderFamily := bystander.FamilyID
	require.NoError(t, store.Insert(ctx, bystander))

	require.NoError(t, store.RevokeFamily(ctx, target.FamilyID, "rotated"))

	// Bystander must be unaffected
	f, err := store.FindByRefreshHash(ctx, bystander.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, f.RevokedAt, "revoking one family must not touch another")
	require.Equal(t, bystanderFamily, f.FamilyID)
}

func TestSessionStore_Insert_RequiresFamilyID(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)

	rec := newRecord(seedUser(t))
	rec.FamilyID = uuid.Nil

	err := store.Insert(ctx, rec)
	require.Error(t, err)
}

func TestSessionStore_Insert_NullableOrgID(t *testing.T) {
	// User with no org yet (signup flow before org creation). OrgID must be
	// persistable as NULL, not crash with "invalid UUID".
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)

	rec := newRecord(userID)
	rec.OrgID = uuid.Nil
	rec.OrgRole = ""

	require.NoError(t, store.Insert(ctx, rec))

	f, err := store.FindByRefreshHash(ctx, rec.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, f.OrgID)
	require.Equal(t, "", f.OrgRole)
}

func TestSessionStore_DeviceLimitEvictsOldestActiveFamily(t *testing.T) {
	ctx := context.Background()
	policy := auth.DefaultSessionPolicy()
	policy.MaxActiveDevices = 2
	store := pgauth.NewSessionStore(testStore, policy)
	userID := seedUser(t)
	base := time.Now().Add(-time.Hour)
	records := []*auth.SessionRecord{newRecord(userID), newRecord(userID), newRecord(userID)}
	for i, rec := range records {
		rec.LastActiveAt = base.Add(time.Duration(i) * time.Minute)
		rec.IdleExpiresAt = rec.LastActiveAt.Add(policy.IdleTimeout)
		require.NoError(t, store.Insert(ctx, rec))
	}

	oldest, err := store.FindByRefreshHash(ctx, records[0].RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, oldest.RevokedAt)
	require.Equal(t, "device_limit_exceeded", oldest.RevokedReason)
	for _, rec := range records[1:] {
		active, err := store.FindByRefreshHash(ctx, rec.RefreshHash)
		require.NoError(t, err)
		require.Nil(t, active.RevokedAt)
	}
}

func TestSessionStore_DeviceLimitSerializesConcurrentLogins(t *testing.T) {
	ctx := context.Background()
	policy := auth.DefaultSessionPolicy()
	policy.MaxActiveDevices = 1
	store := pgauth.NewSessionStore(testStore, policy)
	userID := seedUser(t)
	records := []*auth.SessionRecord{newRecord(userID), newRecord(userID)}
	start := make(chan struct{})
	errs := make(chan error, len(records))
	for _, rec := range records {
		go func(rec *auth.SessionRecord) {
			<-start
			errs <- store.Insert(ctx, rec)
		}(rec)
	}
	close(start)
	for range records {
		require.NoError(t, <-errs)
	}

	var activeCount int
	scanControlPlane(t, &activeCount,
		`SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	require.Equal(t, 1, activeCount)
}

func TestSessionStore_RotateRefreshConsumesAndInsertsAtomically(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	original := newRecord(seedUser(t))
	require.NoError(t, store.Insert(ctx, original))

	var replacement *auth.SessionRecord
	require.NoError(t, store.RotateRefresh(ctx, original.RefreshHash, func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) (*auth.SessionRecord, error) {
		require.Equal(t, original.ID, current.ID)
		replacement = replacementRecord(current, authorization)
		return replacement, nil
	}))

	consumed, err := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, consumed.RevokedAt)
	require.Equal(t, "rotated", consumed.RevokedReason)

	active, err := store.FindByRefreshHash(ctx, replacement.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, active.RevokedAt)
	require.Equal(t, original.FamilyID, active.FamilyID)
}

func TestSessionStore_RotateRefreshResolvesCurrentAuthorization(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	orgID := seedOrganizationMembership(t, userID, "member", time.Now())
	original := newRecord(userID)
	original.OrgID = orgID
	original.OrgRole = "owner"
	original.PlatformRole = "super_admin"

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform_admins (user_id, platform_role, granted_by)
			VALUES ($1, 'support', $1)`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO mfa_devices (id, user_id, device_type, name, secret_encrypted, verified_at)
			VALUES ($1, $2, 'totp', 'Refresh test', 'encrypted-test-secret', NOW())`,
			business.NewID(), userID)
		return err
	}))
	require.NoError(t, store.Insert(ctx, original))

	var got auth.RefreshAuthorization
	var replacement *auth.SessionRecord
	require.NoError(t, store.RotateRefresh(ctx, original.RefreshHash, func(
		current *auth.SessionRecord,
		authorization auth.RefreshAuthorization,
	) (*auth.SessionRecord, error) {
		got = authorization
		replacement = replacementRecord(current, authorization)
		return replacement, nil
	}))

	require.Equal(t, orgID, got.OrgID)
	require.Equal(t, "member", got.OrgRole)
	require.Equal(t, "support", got.PlatformRole)
	require.True(t, got.MFAEnrolled)

	active, err := store.FindByRefreshHash(ctx, replacement.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, orgID, active.OrgID)
	require.Equal(t, "member", active.OrgRole)
	require.Equal(t, "support", active.PlatformRole)
}

func TestSessionStore_RotateRefreshRejectsInactiveUserAndRevokesEverySession(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	original := newRecord(userID)
	bystander := newRecord(userID)
	require.NoError(t, store.Insert(ctx, original))
	require.NoError(t, store.Insert(ctx, bystander))
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `UPDATE users SET status = 'suspended' WHERE uuid = $1`, userID)
		return err
	}))

	callbackCalled := false
	err := store.RotateRefresh(ctx, original.RefreshHash, func(
		_ *auth.SessionRecord,
		_ auth.RefreshAuthorization,
	) (*auth.SessionRecord, error) {
		callbackCalled = true
		return nil, nil
	})
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)
	require.False(t, callbackCalled)

	for _, hash := range [][]byte{original.RefreshHash, bystander.RefreshHash} {
		revoked, findErr := store.FindByRefreshHash(ctx, hash)
		require.NoError(t, findErr)
		require.NotNil(t, revoked.RevokedAt)
		require.Equal(t, auth.RefreshRejectionUserNotActive, revoked.RevokedReason)
	}
}

func TestSessionStore_RotateRefreshRejectsRemovedSelectedMembership(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	orgID := business.NewID()
	original := newRecord(userID)
	original.OrgID = orgID
	require.NoError(t, store.Insert(ctx, original))

	callbackCalled := false
	err := store.RotateRefresh(ctx, original.RefreshHash, func(
		_ *auth.SessionRecord,
		_ auth.RefreshAuthorization,
	) (*auth.SessionRecord, error) {
		callbackCalled = true
		return nil, nil
	})
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)
	require.False(t, callbackCalled)

	revoked, findErr := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, findErr)
	require.NotNil(t, revoked.RevokedAt)
	require.Equal(t, auth.RefreshRejectionOrganizationMembership, revoked.RevokedReason)
}

func TestSessionStore_RotateRefreshAssignsLatestMembershipToOrglessSession(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	seedOrganizationMembership(t, userID, "member", time.Now().Add(-time.Hour))
	latestOrgID := seedOrganizationMembership(t, userID, "admin", time.Now())
	original := newRecord(userID)
	original.OrgID = uuid.Nil
	original.OrgRole = ""
	require.NoError(t, store.Insert(ctx, original))

	var got auth.RefreshAuthorization
	require.NoError(t, store.RotateRefresh(ctx, original.RefreshHash, func(
		current *auth.SessionRecord,
		authorization auth.RefreshAuthorization,
	) (*auth.SessionRecord, error) {
		got = authorization
		return replacementRecord(current, authorization), nil
	}))
	require.Equal(t, latestOrgID, got.OrgID)
	require.Equal(t, "admin", got.OrgRole)
}

func TestSessionStore_RotateRefreshCommitsTerminalPolicyRejection(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	original := newRecord(seedUser(t))
	require.NoError(t, store.Insert(ctx, original))

	err := store.RotateRefresh(ctx, original.RefreshHash, func(
		_ *auth.SessionRecord,
		_ auth.RefreshAuthorization,
	) (*auth.SessionRecord, error) {
		return nil, auth.RejectRefresh(auth.RefreshRejectionAbsoluteLifetime)
	})
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)

	revoked, findErr := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, findErr)
	require.NotNil(t, revoked.RevokedAt)
	require.Equal(t, auth.RefreshRejectionAbsoluteLifetime, revoked.RevokedReason)
}

func TestSessionStore_ExchangeOrganizationPreservesSessionState(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	targetOrgID := seedOrganizationMembership(t, userID, "member", time.Now())
	original := newRecord(userID)
	original.DeviceInfo = map[string]string{"description": "Safari on macOS"}
	original.IPAddress = "203.0.113.10"
	require.NoError(t, store.Insert(ctx, original))

	var got auth.RefreshAuthorization
	require.NoError(t, store.ExchangeOrganization(ctx, userID, original.ID, targetOrgID, func(
		current *auth.SessionRecord,
		authorization auth.RefreshAuthorization,
	) error {
		require.Equal(t, original.ID, current.ID)
		got = authorization
		return nil
	}))
	require.Equal(t, targetOrgID, got.OrgID)
	require.Equal(t, "member", got.OrgRole)

	after, err := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, targetOrgID, after.OrgID)
	require.Equal(t, "member", after.OrgRole)
	require.Equal(t, original.ID, after.ID)
	require.Equal(t, original.FamilyID, after.FamilyID)
	require.Equal(t, original.RefreshHash, after.RefreshHash)
	require.Equal(t, original.IssuedAt, after.IssuedAt)
	require.Equal(t, original.LastActiveAt, after.LastActiveAt)
	require.Equal(t, original.IdleExpiresAt, after.IdleExpiresAt)
	require.Equal(t, original.ExpiresAt, after.ExpiresAt)
	require.Equal(t, original.DeviceInfo, after.DeviceInfo)
	require.Equal(t, original.IPAddress, after.IPAddress)
	require.Nil(t, after.RevokedAt)
}

func TestSessionStore_ExchangeOrganizationRejectsNonMemberWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	currentOrgID := seedOrganizationMembership(t, userID, "admin", time.Now())
	original := newRecord(userID)
	original.OrgID = currentOrgID
	require.NoError(t, store.Insert(ctx, original))

	callbackCalled := false
	err := store.ExchangeOrganization(ctx, userID, original.ID, business.NewID(), func(
		_ *auth.SessionRecord,
		_ auth.RefreshAuthorization,
	) error {
		callbackCalled = true
		return nil
	})
	require.ErrorIs(t, err, auth.ErrOrganizationAccessDenied)
	require.False(t, callbackCalled)

	after, err := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, currentOrgID, after.OrgID)
	require.Equal(t, original.OrgRole, after.OrgRole)
	require.Nil(t, after.RevokedAt)
}

func TestSessionStore_ExchangeOrganizationRacingRefreshNeverTriggersReplayRevocation(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	currentOrgID := seedOrganizationMembership(t, userID, "admin", time.Now().Add(-time.Hour))
	targetOrgID := seedOrganizationMembership(t, userID, "member", time.Now())
	original := newRecord(userID)
	original.OrgID = currentOrgID
	require.NoError(t, store.Insert(ctx, original))

	start := make(chan struct{})
	refreshResult := make(chan error, 1)
	switchResult := make(chan error, 1)
	var replacement *auth.SessionRecord
	go func() {
		<-start
		refreshResult <- store.RotateRefresh(ctx, original.RefreshHash, func(
			current *auth.SessionRecord,
			authorization auth.RefreshAuthorization,
		) (*auth.SessionRecord, error) {
			replacement = replacementRecord(current, authorization)
			return replacement, nil
		})
	}()
	go func() {
		<-start
		switchResult <- store.ExchangeOrganization(ctx, userID, original.ID, targetOrgID, func(
			_ *auth.SessionRecord,
			authorization auth.RefreshAuthorization,
		) error {
			if authorization.OrgID != targetOrgID {
				return fmt.Errorf("organization exchange resolved %s, want %s", authorization.OrgID, targetOrgID)
			}
			return nil
		})
	}()
	close(start)

	require.NoError(t, <-refreshResult)
	switchErr := <-switchResult
	require.True(t, switchErr == nil || errors.Is(switchErr, auth.ErrSessionUnavailable),
		"switch either wins the row lock or observes the rotated session: %v", switchErr)
	require.NotNil(t, replacement)

	active, err := store.FindByRefreshHash(ctx, replacement.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, active.RevokedAt, "organization exchange must never cause replay revocation")
	require.Equal(t, original.FamilyID, active.FamilyID)
	require.Contains(t, []uuid.UUID{currentOrgID, targetOrgID}, active.OrgID)
	var activeCount int
	scanControlPlane(t, &activeCount,
		`SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	require.Equal(t, 1, activeCount)
}

func TestAuthorizationInvalidation_OrganizationRoleChangeIsScopedAndAtomic(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	changedOrgID := seedOrganizationMembership(t, userID, "member", time.Now())
	otherOrgID := seedOrganizationMembership(t, userID, "member", time.Now().Add(-time.Hour))

	changedOrgSession := newRecord(userID)
	changedOrgSession.OrgID = changedOrgID
	orglessSession := newRecord(userID)
	orglessSession.OrgID = uuid.Nil
	orglessSession.OrgRole = ""
	otherOrgSession := newRecord(userID)
	otherOrgSession.OrgID = otherOrgID
	for _, rec := range []*auth.SessionRecord{changedOrgSession, orglessSession, otherOrgSession} {
		require.NoError(t, store.Insert(ctx, rec))
	}

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			UPDATE organization_members
			SET role = 'admin'
			WHERE org_id = $1 AND user_id = $2`, changedOrgID, userID)
		return err
	}))

	for _, rec := range []*auth.SessionRecord{changedOrgSession, orglessSession} {
		revoked, err := store.FindByRefreshHash(ctx, rec.RefreshHash)
		require.NoError(t, err)
		require.NotNil(t, revoked.RevokedAt)
		require.Equal(t, "organization_membership_changed", revoked.RevokedReason)
	}
	active, err := store.FindByRefreshHash(ctx, otherOrgSession.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, active.RevokedAt, "a role change in one tenant must not revoke another selected tenant")

	removedOrgSession := newRecord(userID)
	removedOrgSession.OrgID = changedOrgID
	newOrglessSession := newRecord(userID)
	newOrglessSession.OrgID = uuid.Nil
	newOrglessSession.OrgRole = ""
	for _, rec := range []*auth.SessionRecord{removedOrgSession, newOrglessSession} {
		require.NoError(t, store.Insert(ctx, rec))
	}
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			DELETE FROM organization_members
			WHERE org_id = $1 AND user_id = $2`, changedOrgID, userID)
		return err
	}))
	for _, rec := range []*auth.SessionRecord{removedOrgSession, newOrglessSession} {
		revoked, err := store.FindByRefreshHash(ctx, rec.RefreshHash)
		require.NoError(t, err)
		require.NotNil(t, revoked.RevokedAt)
		require.Equal(t, "organization_membership_revoked", revoked.RevokedReason)
	}
	active, err = store.FindByRefreshHash(ctx, otherOrgSession.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, active.RevokedAt, "removing one tenant must not revoke another selected tenant")
}

func TestAuthorizationInvalidation_OrganizationMembershipAdditionRevokesOrglessSessions(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	existingOrgID := seedOrganizationMembership(t, userID, "member", time.Now())
	newOrgID := business.NewID()
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			INSERT INTO organizations (id, name, slug, owner_id)
			VALUES ($1, 'New Refresh Organization', $2, $3)`,
			newOrgID, fmt.Sprintf("refresh-%s", newOrgID), userID)
		return err
	}))

	orglessSession := newRecord(userID)
	orglessSession.OrgID = uuid.Nil
	orglessSession.OrgRole = ""
	existingOrgSession := newRecord(userID)
	existingOrgSession.OrgID = existingOrgID
	for _, rec := range []*auth.SessionRecord{orglessSession, existingOrgSession} {
		require.NoError(t, store.Insert(ctx, rec))
	}

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			INSERT INTO organization_members (org_id, user_id, role)
			VALUES ($1, $2, 'member')`, newOrgID, userID)
		return err
	}))

	revoked, err := store.FindByRefreshHash(ctx, orglessSession.RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	require.Equal(t, "organization_membership_added", revoked.RevokedReason)
	active, err := store.FindByRefreshHash(ctx, existingOrgSession.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, active.RevokedAt, "adding a tenant must not revoke another selected tenant")
}

func TestAuthorizationInvalidation_PlatformRoleChangeRevokesAllUserSessions(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	otherUserID := seedUser(t)
	targetSessions := []*auth.SessionRecord{newRecord(userID), newRecord(userID)}
	bystander := newRecord(otherUserID)
	for _, rec := range append(targetSessions, bystander) {
		require.NoError(t, store.Insert(ctx, rec))
	}

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			INSERT INTO platform_admins (user_id, platform_role, granted_by)
			VALUES ($1, 'support', $1)`, userID)
		return err
	}))

	for _, rec := range targetSessions {
		revoked, err := store.FindByRefreshHash(ctx, rec.RefreshHash)
		require.NoError(t, err)
		require.NotNil(t, revoked.RevokedAt)
		require.Equal(t, "platform_role_changed", revoked.RevokedReason)
	}
	active, err := store.FindByRefreshHash(ctx, bystander.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, active.RevokedAt)

	updatedRoleSession := newRecord(userID)
	require.NoError(t, store.Insert(ctx, updatedRoleSession))
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			UPDATE platform_admins
			SET platform_role = 'billing'
			WHERE user_id = $1`, userID)
		return err
	}))
	updatedRole, err := store.FindByRefreshHash(ctx, updatedRoleSession.RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, updatedRole.RevokedAt)
	require.Equal(t, "platform_role_changed", updatedRole.RevokedReason)

	removedRoleSession := newRecord(userID)
	require.NoError(t, store.Insert(ctx, removedRoleSession))
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `DELETE FROM platform_admins WHERE user_id = $1`, userID)
		return err
	}))
	removedRole, err := store.FindByRefreshHash(ctx, removedRoleSession.RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, removedRole.RevokedAt)
	require.Equal(t, "platform_role_changed", removedRole.RevokedReason)
}

func TestAuthorizationInvalidation_MFAEnrollmentIgnoresUnverifiedDeviceThenRevokes(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	original := newRecord(userID)
	require.NoError(t, store.Insert(ctx, original))
	deviceID := business.NewID()

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			INSERT INTO mfa_devices (id, user_id, device_type, name, secret_encrypted)
			VALUES ($1, $2, 'totp', 'Pending device', 'encrypted-test-secret')`, deviceID, userID)
		return err
	}))
	stillActive, err := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, stillActive.RevokedAt)

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `UPDATE mfa_devices SET verified_at = NOW() WHERE id = $1`, deviceID)
		return err
	}))
	revoked, err := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	require.Equal(t, "mfa_enrollment_changed", revoked.RevokedReason)

	postEnrollment := newRecord(userID)
	require.NoError(t, store.Insert(ctx, postEnrollment))
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `DELETE FROM mfa_devices WHERE id = $1`, deviceID)
		return err
	}))
	revoked, err = store.FindByRefreshHash(ctx, postEnrollment.RefreshHash)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	require.Equal(t, "mfa_enrollment_changed", revoked.RevokedReason)
}

func TestAuthorizationInvalidation_RollsBackWithAuthorizationMutation(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	original := newRecord(userID)
	require.NoError(t, store.Insert(ctx, original))
	injected := errors.New("rollback authorization mutation")

	err := testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform_admins (user_id, platform_role, granted_by)
			VALUES ($1, 'support', $1)`, userID); err != nil {
			return err
		}
		return injected
	})
	require.ErrorIs(t, err, injected)

	active, err := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, active.RevokedAt, "trigger revocation must roll back with the failed role mutation")
	var platformAdminCount int
	scanControlPlane(t, &platformAdminCount, `SELECT COUNT(*) FROM platform_admins WHERE user_id = $1`, userID)
	require.Zero(t, platformAdminCount)
}

func TestAuthorizationInvalidationFunctionHasPinnedAuthority(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		var owner string
		var securityDefiner, tenantCanExecute bool
		err := tx.QueryRow(ctx, `
			SELECT owner.rolname,
			       proc.prosecdef,
			       has_function_privilege(
			           'app_tenant',
			           'public.invalidate_authorization_sessions()',
			           'EXECUTE'
			       )
			FROM pg_proc proc
			JOIN pg_namespace namespace ON namespace.oid = proc.pronamespace
			JOIN pg_roles owner ON owner.oid = proc.proowner
			WHERE namespace.nspname = 'public'
			  AND proc.proname = 'invalidate_authorization_sessions'`).Scan(
			&owner,
			&securityDefiner,
			&tenantCanExecute,
		)
		require.NoError(t, err)
		require.Equal(t, "app_control_plane", owner)
		require.True(t, securityDefiner)
		require.False(t, tenantCanExecute)
		return nil
	}))
}

func TestSessionStore_RotateRefreshRollsBackConsumptionWhenInsertFails(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	original := newRecord(userID)
	conflict := newRecord(userID)
	require.NoError(t, store.Insert(ctx, original))
	require.NoError(t, store.Insert(ctx, conflict))

	err := store.RotateRefresh(ctx, original.RefreshHash, func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) (*auth.SessionRecord, error) {
		replacement := replacementRecord(current, authorization)
		replacement.RefreshHash = append([]byte(nil), conflict.RefreshHash...)
		return replacement, nil
	})
	require.Error(t, err, "duplicate successor hash must fail insertion")

	stillActive, err := store.FindByRefreshHash(ctx, original.RefreshHash)
	require.NoError(t, err)
	require.Nil(t, stillActive.RevokedAt,
		"successor insertion failure must roll back presented-token consumption")
}

func TestSessionStore_ConcurrentRefreshHasOneWinnerAndReplayRevokesUser(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	original := newRecord(userID)
	bystander := newRecord(userID)
	require.NoError(t, store.Insert(ctx, original))
	require.NoError(t, store.Insert(ctx, bystander))

	type result struct {
		replacement *auth.SessionRecord
		err         error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			var next *auth.SessionRecord
			err := store.RotateRefresh(ctx, original.RefreshHash, func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) (*auth.SessionRecord, error) {
				next = replacementRecord(current, authorization)
				return next, nil
			})
			results <- result{replacement: next, err: err}
		}()
	}
	close(start)

	var winner *auth.SessionRecord
	var successCount, reuseCount int
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			successCount++
			winner = got.replacement
		case errors.Is(got.err, auth.ErrRefreshReuse):
			reuseCount++
		default:
			t.Fatalf("unexpected concurrent refresh result: %v", got.err)
		}
	}
	require.Equal(t, 1, successCount)
	require.Equal(t, 1, reuseCount)
	require.NotNil(t, winner)

	var activeCount int
	scanControlPlane(t, &activeCount,
		`SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	require.Zero(t, activeCount,
		"the losing replay must revoke the winner and every other active user session")

	winnerRow, err := store.FindByRefreshHash(ctx, winner.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, "refresh-reuse-all-sessions", winnerRow.RevokedReason)
	bystanderRow, err := store.FindByRefreshHash(ctx, bystander.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, "refresh-reuse-all-sessions", bystanderRow.RevokedReason)
}

// TestRLS_Sessions_CrossUserBlocked — Phase 2H. Two users each get a
// session row; user A's WithUserTx must see only their own session.
// User B's row stays invisible from A's scope. Un-wrapped reads return
// zero (fail-closed). Refresh-token-hash lookup uses WithControlPlane and
// CAN see both — that's the only correct shape (the lookup doesn't
// know the user yet).
func TestRLS_Sessions_CrossUserBlocked(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)

	userA := seedUser(t)
	userB := seedUser(t)

	recA := newRecord(userA)
	recB := newRecord(userB)
	require.NoError(t, store.Insert(ctx, recA))
	require.NoError(t, store.Insert(ctx, recB))

	// Cross-user probe via raw tx: from A's WithUserTx, ask for
	// any sessions whose user_id matches B. RLS hides B's row.
	require.NoError(t, testStore.WithUserTx(ctx, userA.String(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck
		var count int
		require.NoError(t, tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM sessions WHERE user_id = $1`, userB,
		).Scan(&count))
		require.Equal(t, 0, count,
			"RLS must hide B's sessions from A's WithUserTx")
		return nil
	}))

	// Un-wrapped: zero rows. The pool's BeforeAcquire SET ROLE
	// app_tenant + no app.current_user_id GUC = fail-closed.
	var noWrapCount int
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM sessions WHERE user_id = $1`, userA,
	).Scan(&noWrapCount))
	require.Equal(t, 0, noWrapCount,
		"un-wrapped session read must return ZERO rows (RLS fail-closed)")

	// FindByRefreshHash (the production refresh-token lookup path)
	// uses WithControlPlane and CAN see any user's row by design — that's
	// what makes the auth flow work without knowing the user yet.
	foundA, err := store.FindByRefreshHash(ctx, recA.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, userA, foundA.UserID)
	foundB, err := store.FindByRefreshHash(ctx, recB.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, userB, foundB.UserID)
}
