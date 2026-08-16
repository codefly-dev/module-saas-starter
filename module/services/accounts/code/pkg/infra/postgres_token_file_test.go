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
