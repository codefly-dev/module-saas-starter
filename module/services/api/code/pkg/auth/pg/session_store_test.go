package pgauth_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/wool"

	"api/pkg/auth"
	pgauth "api/pkg/auth/pg"
	"api/pkg/business"
)

var testPool *pgxpool.Pool

// TestMain boots a real postgres (+ vault) via codefly WithDependencies and
// holds a pgxpool for the whole package. Matches the pattern in
// pkg/business/service_test.go so the two suites can run back-to-back.
func TestMain(m *testing.M) {
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
		os.Exit(1)
	}
	defer deps.Destroy(ctx)

	if _, err := codefly.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init failed: %v\n", err)
		os.Exit(1)
	}

	conn, err := codefly.For(ctx).Service("store").Secret("postgres", "connection")
	if err != nil {
		fmt.Fprintf(os.Stderr, "get connection string: %v\n", err)
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgxpool.New: %v\n", err)
		os.Exit(1)
	}
	testPool = pool

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// seedUser inserts a minimum users row so sessions FK is happy.
func seedUser(t *testing.T) uuid.UUID {
	t.Helper()
	id := business.NewID()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO users (uuid, primary_email, status)
		VALUES ($1, $2, 'active')`,
		id, fmt.Sprintf("user-%s@test.local", id.String()))
	require.NoError(t, err)
	return id
}

func newRecord(userID uuid.UUID) *auth.SessionRecord {
	hash := sha256.Sum256([]byte(uuid.Must(uuid.NewV7()).String()))
	return &auth.SessionRecord{
		ID:           business.NewID(),
		UserID:       userID,
		FamilyID:     business.NewID(),
		RefreshHash:  hash[:],
		IssuedAt:     time.Now(),
		ExpiresAt:    time.Now().Add(7 * 24 * time.Hour),
		OrgRole:      "admin",
		PlatformRole: "super_admin",
	}
}

func TestSessionStore_InsertAndFind(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testPool)

	userID := seedUser(t)
	rec := newRecord(userID)

	require.NoError(t, store.Insert(ctx, rec))

	found, err := store.FindByRefreshHash(ctx, rec.RefreshHash)
	require.NoError(t, err)
	require.Equal(t, rec.ID, found.ID)
	require.Equal(t, rec.UserID, found.UserID)
	require.Equal(t, rec.FamilyID, found.FamilyID)
	require.Equal(t, rec.OrgRole, found.OrgRole)
	require.Equal(t, rec.PlatformRole, found.PlatformRole)
	require.Nil(t, found.RevokedAt)
}

func TestSessionStore_FindByRefreshHash_NotFound(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testPool)

	hash := sha256.Sum256([]byte("never-existed"))
	_, err := store.FindByRefreshHash(ctx, hash[:])
	require.ErrorIs(t, err, auth.ErrRefreshRevoked,
		"unknown hash must return ErrRefreshRevoked for oracle resistance")
}

func TestSessionStore_RevokeFamily(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testPool)
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
	store := pgauth.NewSessionStore(testPool)
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
	store := pgauth.NewSessionStore(testPool)

	rec := newRecord(seedUser(t))
	rec.FamilyID = uuid.Nil

	err := store.Insert(ctx, rec)
	require.Error(t, err)
}

func TestSessionStore_Insert_NullableOrgID(t *testing.T) {
	// User with no org yet (signup flow before org creation). OrgID must be
	// persistable as NULL, not crash with "invalid UUID".
	ctx := context.Background()
	store := pgauth.NewSessionStore(testPool)
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
