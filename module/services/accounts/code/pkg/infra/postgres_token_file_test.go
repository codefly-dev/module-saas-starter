package infra

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestTokenFileBeforeConnectUnsetKeepsEmbeddedPassword(t *testing.T) {
	t.Setenv(databaseTokenFileEnv, "")
	require.Nil(t, accessTokenBeforeConnect(tokenFileAccessTokenProvider()),
		"no token file configured must leave the URL-embedded password in force")
}

func TestTokenFileBeforeConnectRereadsTokenPerConnection(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("  first-token\n"), 0o600))
	t.Setenv(databaseTokenFileEnv, tokenPath)

	hook := accessTokenBeforeConnect(tokenFileAccessTokenProvider())
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

	err = accessTokenBeforeConnect(tokenFileAccessTokenProvider())(context.Background(), connConfig)
	require.ErrorContains(t, err, "empty")
}

func TestTokenFileBeforeConnectMissingFileFailsLoud(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv(databaseTokenFileEnv, tokenPath)

	connConfig, err := pgx.ParseConfig("postgresql://app@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)

	err = accessTokenBeforeConnect(tokenFileAccessTokenProvider())(context.Background(), connConfig)
	require.Error(t, err)
}

func TestTokenFileBeforeConnectRejectsOversizedFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, make([]byte, maxDatabaseTokenBytes+1), 0o600))
	t.Setenv(databaseTokenFileEnv, tokenPath)

	connConfig, err := pgx.ParseConfig("postgresql://app@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)

	err = accessTokenBeforeConnect(tokenFileAccessTokenProvider())(context.Background(), connConfig)
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

	err = accessTokenBeforeConnect(tokenFileAccessTokenProvider())(ctx, connConfig)
	require.ErrorIs(t, err, context.Canceled,
		"a cancelled connection attempt must not block on the token read")
	require.Equal(t, "embedded", connConfig.Password,
		"a cancelled read must not partially apply a password")
}

// The legacy pool retains its pgx hook; request pools use the shared provider.
func TestConfigureConnectionWiresHookWhenConfigured(t *testing.T) {
	t.Setenv(databaseTokenFileEnv, filepath.Join(t.TempDir(), "token"))

	config, err := configureConnection("postgresql://app:pw@localhost:5432/db?sslmode=disable", accessTokenBeforeConnect(tokenFileAccessTokenProvider()))
	require.NoError(t, err)
	require.NotNil(t, config.BeforeConnect,
		"a configured token file must reach the pool as a BeforeConnect hook")
}

func TestConfigureConnectionHasNoHookWhenUnset(t *testing.T) {
	t.Setenv(databaseTokenFileEnv, "")

	config, err := configureConnection("postgresql://app:pw@localhost:5432/db?sslmode=disable", accessTokenBeforeConnect(tokenFileAccessTokenProvider()))
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

func TestOpenScopedBoundaryRejectsMissingAndSharedCapabilities(t *testing.T) {
	clearDatabaseEnvironment(t)
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "")
	_, _, err := openScopedBoundary(context.Background(), "", "", nil)
	require.ErrorContains(t, err, "connections are required")
	connection := "postgresql://same@localhost/database?sslmode=disable"
	_, _, err = openScopedBoundary(context.Background(), connection, connection, nil)
	require.ErrorContains(t, err, "distinct database roles")
}

func TestTokenFileAccessTokenProviderRetainsOneProjectedCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(databaseTokenFileEnv, path)
	provider := tokenFileAccessTokenProvider()
	require.NotNil(t, provider)
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o600))
	token, err := provider(context.Background(), "reader")
	require.NoError(t, err)
	require.Equal(t, "first", token)
	require.NoError(t, os.WriteFile(path, []byte("second"), 0o600))
	token, err = provider(context.Background(), "writer")
	require.NoError(t, err)
	require.Equal(t, "second", token)
}

func TestTokenFilesSelectPhysicalLoginAndRotateIndependently(t *testing.T) {
	clearDatabaseEnvironment(t)
	dir := t.TempDir()
	paths := map[string]string{
		"reader@example.invalid": filepath.Join(dir, "reader"),
		"writer":                 filepath.Join(dir, "writer"),
		"control":                filepath.Join(dir, "control"),
	}
	for login, path := range paths {
		require.NoError(t, os.WriteFile(path, []byte(login+"-first"), 0o600))
	}
	raw, err := json.Marshal(paths)
	require.NoError(t, err)
	t.Setenv(databaseTokenFilesEnv, string(raw))
	provider := tokenFileAccessTokenProvider()
	require.NotNil(t, provider)
	for login := range paths {
		token, err := provider(context.Background(), login)
		require.NoError(t, err)
		require.Equal(t, login+"-first", token)
	}
	path := paths["writer"]
	require.NoError(t, os.WriteFile(path+".next", []byte("writer-second"), 0o600))
	require.NoError(t, os.Rename(path+".next", path))
	for login := range paths {
		config, err := pgx.ParseConfig("postgres://localhost/test?sslmode=disable")
		require.NoError(t, err)
		config.User = login
		config.Password = "embedded"
		require.NoError(t, accessTokenBeforeConnect(provider)(context.Background(), config))
		want := login + "-first"
		if login == "writer" {
			want = "writer-second"
		}
		require.Equal(t, want, config.Password)
	}
	for _, login := range []string{"", "unknown", "Reader@example.invalid", " writer"} {
		token, err := provider(context.Background(), login)
		require.ErrorContains(t, err, "no database token file")
		require.Empty(t, token)
	}
	require.NoError(t, os.Remove(paths["control"]))
	token, err := provider(context.Background(), "control")
	require.Error(t, err)
	require.Empty(t, token)
}

func TestTokenFilesRejectInvalidOrAmbiguousConfiguration(t *testing.T) {
	clearDatabaseEnvironment(t)
	for _, raw := range []string{
		" ", "null", "[]", "{}", `{"reader":"relative"}`,
		`{"reader":null}`, `{"reader":3}`, `{"":"/token"}`,
		`{" reader":"/token"}`, `{"reader":"/one","reader":"/two"}`,
		`{"reader":"/token"} {}`, `{"reader":"/token\u0000"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(databaseTokenFilesEnv, raw)
			provider := tokenFileAccessTokenProvider()
			require.NotNil(t, provider, "invalid configuration must not fall back to embedded credentials")
			token, err := provider(context.Background(), "reader")
			require.ErrorContains(t, err, "POSTGRES_TOKEN_FILES")
			require.Empty(t, token)
		})
	}
	t.Setenv(databaseTokenFileEnv, "/single")
	t.Setenv(databaseTokenFilesEnv, `{"reader":"/per-login"}`)
	token, err := tokenFileAccessTokenProvider()(context.Background(), "reader")
	require.ErrorContains(t, err, "mutually exclusive")
	require.Empty(t, token)
}

func TestOpenScopedBoundaryRetainsProxySeparation(t *testing.T) {
	clearDatabaseEnvironment(t)
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "local-identity-proxy")
	_, _, err := openScopedBoundary(context.Background(), proxyURL(), proxyURL(), nil)
	require.ErrorContains(t, err, "distinct private identity sockets")
}
