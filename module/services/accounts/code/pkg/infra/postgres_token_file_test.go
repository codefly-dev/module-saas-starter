package infra

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestTokenFileBeforeConnectUnsetKeepsEmbeddedPassword(t *testing.T) {
	t.Setenv(databaseTokenFileEnv, "")
	require.Nil(t, tokenFileBeforeConnect(),
		"no token file configured must leave the URL-embedded password in force")
}

func TestTokenFileBeforeConnectRereadsTokenPerConnection(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("  first-token\n"), 0o600))
	t.Setenv(databaseTokenFileEnv, tokenPath)

	hook := tokenFileBeforeConnect()
	require.NotNil(t, hook)

	connConfig, err := pgx.ParseConfig("postgresql://app:stale-embedded@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)

	require.NoError(t, hook(context.Background(), connConfig))
	require.Equal(t, "first-token", connConfig.Password,
		"the hook must replace the stale embedded password with the current token")

	// The sidecar rotates the token in place; the next connection attempt must
	// pick it up rather than reuse the value read at pool construction.
	require.NoError(t, os.WriteFile(tokenPath, []byte("second-token"), 0o600))
	require.NoError(t, hook(context.Background(), connConfig))
	require.Equal(t, "second-token", connConfig.Password)
}

func TestTokenFileBeforeConnectEmptyFileFailsLoud(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("   \n"), 0o600))
	t.Setenv(databaseTokenFileEnv, tokenPath)

	connConfig, err := pgx.ParseConfig("postgresql://app@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)

	err = tokenFileBeforeConnect()(context.Background(), connConfig)
	require.ErrorContains(t, err, "empty")
}

func TestTokenFileBeforeConnectMissingFileFailsLoud(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv(databaseTokenFileEnv, tokenPath)

	connConfig, err := pgx.ParseConfig("postgresql://app@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)

	err = tokenFileBeforeConnect()(context.Background(), connConfig)
	require.Error(t, err)
}

func TestTokenFileBeforeConnectRejectsOversizedFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, make([]byte, maxDatabaseTokenBytes+1), 0o600))
	t.Setenv(databaseTokenFileEnv, tokenPath)

	connConfig, err := pgx.ParseConfig("postgresql://app@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)

	err = tokenFileBeforeConnect()(context.Background(), connConfig)
	require.ErrorContains(t, err, "exceeds")
}

func TestTokenFileBeforeConnectHonorsContextCancellation(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("token"), 0o600))
	t.Setenv(databaseTokenFileEnv, tokenPath)

	connConfig, err := pgx.ParseConfig("postgresql://app:embedded@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = tokenFileBeforeConnect()(ctx, connConfig)
	require.ErrorIs(t, err, context.Canceled,
		"a cancelled connection attempt must not block on the token read")
	require.Equal(t, "embedded", connConfig.Password,
		"a cancelled read must not partially apply a password")
}

// configureConnection is the single seam that attaches the rotation hook to
// every pool; assert the wiring itself, since the hook's own behavior is covered
// above but a dropped assignment would silently disable rotation.
func TestConfigureConnectionWiresHookWhenConfigured(t *testing.T) {
	t.Setenv(databaseTokenFileEnv, filepath.Join(t.TempDir(), "token"))

	config, err := configureConnection("postgresql://app:pw@localhost:5432/db?sslmode=disable", tokenFileBeforeConnect())
	require.NoError(t, err)
	require.NotNil(t, config.BeforeConnect,
		"a configured token file must reach the pool as a BeforeConnect hook")
}

func TestConfigureConnectionHasNoHookWhenUnset(t *testing.T) {
	t.Setenv(databaseTokenFileEnv, "")

	config, err := configureConnection("postgresql://app:pw@localhost:5432/db?sslmode=disable", tokenFileBeforeConnect())
	require.NoError(t, err)
	require.Nil(t, config.BeforeConnect,
		"local password mode must leave the pool without a connection hook")
}

func TestConfigureConnectionRejectsEmptyURL(t *testing.T) {
	_, err := configureConnection("  ", nil)
	require.ErrorContains(t, err, "connection URL is required")
}

func TestOpenScopedBoundaryRejectsNilContext(t *testing.T) {
	var nilCtx context.Context
	_, _, err := openScopedBoundary(nilCtx, "read-only", "read-write", nil)
	require.ErrorContains(t, err, "context is required")
}
