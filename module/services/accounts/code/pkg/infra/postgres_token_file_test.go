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

func TestResolveDatabaseTokenSourcePrecedence(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")

	t.Run("neither set keeps the embedded password", func(t *testing.T) {
		t.Setenv(databaseTokenFileEnv, "")
		t.Setenv(databaseTokenSourceEnv, "")
		source, path, err := resolveDatabaseTokenSource()
		require.NoError(t, err)
		require.Equal(t, databaseTokenPassword, source)
		require.Empty(t, path)
	})

	t.Run("file alone selects the fallback lane", func(t *testing.T) {
		t.Setenv(databaseTokenFileEnv, tokenPath)
		t.Setenv(databaseTokenSourceEnv, "")
		source, path, err := resolveDatabaseTokenSource()
		require.NoError(t, err)
		require.Equal(t, databaseTokenFileSource, source)
		require.Equal(t, tokenPath, path)
	})

	t.Run("in-process source wins over a leftover token file", func(t *testing.T) {
		// The migration footgun: an operator sets the in-process source and
		// removes the sidecar but leaves POSTGRES_TOKEN_FILE pointing at the now
		// vanished file. The explicit source must win so the pool does not brick
		// on a stale/missing file.
		t.Setenv(databaseTokenFileEnv, tokenPath)
		t.Setenv(databaseTokenSourceEnv, "azure")
		source, path, err := resolveDatabaseTokenSource()
		require.NoError(t, err)
		require.Equal(t, databaseTokenAzure, source,
			"an explicit POSTGRES_TOKEN_SOURCE is the primary path and must win over the fallback file")
		require.Equal(t, tokenPath, path, "the ignored file path is still surfaced so the caller can warn")
	})

	t.Run("unsupported source fails loud without falling back to the file", func(t *testing.T) {
		t.Setenv(databaseTokenFileEnv, tokenPath)
		t.Setenv(databaseTokenSourceEnv, "gcp")
		source, path, err := resolveDatabaseTokenSource()
		require.ErrorContains(t, err, "unsupported")
		require.ErrorContains(t, err, "gcp")
		require.Equal(t, databaseTokenPassword, source)
		require.Empty(t, path)
	})
}

func TestDatabaseTokenBeforeConnectUnsupportedSourceFailsLoud(t *testing.T) {
	t.Setenv(databaseTokenFileEnv, "")
	t.Setenv(databaseTokenSourceEnv, "gcp")

	hook, err := databaseTokenBeforeConnect(context.Background())
	require.Nil(t, hook)
	require.ErrorContains(t, err, "unsupported")
	require.ErrorContains(t, err, "gcp")
}

func TestDatabaseTokenBeforeConnectAzureSourceSelectsInProcessMinter(t *testing.T) {
	t.Setenv(databaseTokenFileEnv, "")
	t.Setenv(databaseTokenSourceEnv, "azure")

	// The credential chain builds without any live Azure environment; minting a
	// token (which requires a real identity) is deferred to the per-connection
	// hook and is exercised in deployment, not here.
	hook, err := databaseTokenBeforeConnect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, hook,
		"the azure source must select the in-process azidentity minter")
}

func TestDatabaseTokenBeforeConnectFileLaneReadsToken(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("file-token"), 0o600))
	t.Setenv(databaseTokenFileEnv, tokenPath)
	t.Setenv(databaseTokenSourceEnv, "")

	hook, err := databaseTokenBeforeConnect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, hook)

	connConfig, err := pgx.ParseConfig("postgresql://app:stale@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)
	require.NoError(t, hook(context.Background(), connConfig))
	require.Equal(t, "file-token", connConfig.Password,
		"with no in-process source, the file lane resolves the credential")
}
