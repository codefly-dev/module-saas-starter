package devvalidator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"api/pkg/auth"
	devvalidator "api/pkg/auth/dev"
)

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dev-admin.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestNew_ParsesUsers(t *testing.T) {
	path := writeFixture(t, `
users:
  - email: admin@acme.com
    provider: email
    provider_id: dev-admin
  - email: alice@acme.com
    provider: email
    provider_id: dev-alice
`)
	v, err := devvalidator.New(path)
	require.NoError(t, err)
	require.NotNil(t, v)
}

func TestValidate_KnownSeed(t *testing.T) {
	path := writeFixture(t, `
users:
  - email: admin@acme.com
    provider: email
    provider_id: dev-admin
`)
	v, _ := devvalidator.New(path)
	claims, err := v.Validate(context.Background(), "dev-admin")
	require.NoError(t, err)
	require.Equal(t, "email", claims.Provider)
	require.Equal(t, "dev-admin", claims.Subject)
	require.Equal(t, "admin@acme.com", claims.Email)
	require.NoError(t, claims.Valid())
}

func TestValidate_UnknownTokenReturnsSentinel(t *testing.T) {
	path := writeFixture(t, `
users:
  - email: admin@acme.com
    provider: email
    provider_id: dev-admin
`)
	v, _ := devvalidator.New(path)
	_, err := v.Validate(context.Background(), "not-a-real-id")
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrUnknownIdentity))
}

func TestValidate_EmptyToken(t *testing.T) {
	path := writeFixture(t, `
users:
  - email: admin@acme.com
    provider: email
    provider_id: dev-admin
`)
	v, _ := devvalidator.New(path)
	_, err := v.Validate(context.Background(), "")
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrMissingSubject))
}

func TestNew_EmptyFixtureRejected(t *testing.T) {
	path := writeFixture(t, `users: []`)
	_, err := devvalidator.New(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no usable users")
}

func TestNew_FileNotFound(t *testing.T) {
	_, err := devvalidator.New("/nonexistent/path/fixture.yaml")
	require.Error(t, err)
}

func TestNew_MalformedYAML(t *testing.T) {
	path := writeFixture(t, "users: [not valid yaml")
	_, err := devvalidator.New(path)
	require.Error(t, err)
}

func TestValidate_SkipsIncompleteEntries(t *testing.T) {
	path := writeFixture(t, `
users:
  - email: admin@acme.com
    provider: email
    provider_id: dev-admin
  - email: incomplete@acme.com
    provider: email
    # missing provider_id — must be skipped
  - email: ""
    provider: email
    provider_id: empty-email
`)
	v, err := devvalidator.New(path)
	require.NoError(t, err)

	_, err = v.Validate(context.Background(), "dev-admin")
	require.NoError(t, err)
}

func TestValidate_DefaultsToProviderDev(t *testing.T) {
	path := writeFixture(t, `
users:
  - email: admin@acme.com
    provider_id: dev-admin
`)
	v, _ := devvalidator.New(path)
	claims, err := v.Validate(context.Background(), "dev-admin")
	require.NoError(t, err)
	require.Equal(t, "dev", claims.Provider, "provider must default to 'dev' when omitted")
}
