package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type moduleRegistrationStore struct{ business.Store }

// The mint records its credential issuance transactionally, so the fake has to
// carry a transaction boundary; no audit emitter is wired, so the emit is a
// no-op inside it.
func (*moduleRegistrationStore) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

type recordingRegistrationMinter struct {
	prefix string
	err    error
}

func (m *recordingRegistrationMinter) MintModuleRegistration(prefix string) (string, time.Time, error) {
	if m.err != nil {
		return "", time.Time{}, m.err
	}
	m.prefix = prefix
	return "signed-token-for-" + prefix, time.Unix(1700000000, 0).UTC(), nil
}

func registrationDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// installModuleRegistrar wires a real business.Service holding the declared
// policy, so these tests exercise the same authorization path the RPC serves.
func installModuleRegistrar(t *testing.T, declared string) *recordingRegistrationMinter {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })

	svc, err := business.NewService(&moduleRegistrationStore{})
	require.NoError(t, err)
	secrets, err := business.ParseRegistrationSecrets(declared)
	require.NoError(t, err)
	minter := &recordingRegistrationMinter{}
	svc.SetModuleRegistrar(minter, secrets)
	WithService(svc)
	return minter
}

func TestMintModuleRegistrationIssuesForDeclaredModule(t *testing.T) {
	minter := installModuleRegistrar(t, "documents:"+registrationDigest("documents-secret"))

	resp, err := ModuleCapabilitiesSingleton().MintModuleRegistration(context.Background(),
		&gen.ModuleMintRegistrationRequest{Prefix: "documents", Secret: "documents-secret"})

	require.NoError(t, err)
	require.Equal(t, "signed-token-for-documents", resp.GetToken())
	require.Equal(t, "documents", minter.prefix)
	require.Equal(t, int64(1700000000), resp.GetExpiresAt().AsTime().Unix())
}

// The per-module binding is the property the shared cluster token could not
// give: holding "documents"' secret must not yield a token for another prefix.
func TestMintModuleRegistrationBindsSecretToPrefix(t *testing.T) {
	minter := installModuleRegistrar(t, "documents:"+registrationDigest("documents-secret")+",billing:"+registrationDigest("billing-secret"))

	_, err := ModuleCapabilitiesSingleton().MintModuleRegistration(context.Background(),
		&gen.ModuleMintRegistrationRequest{Prefix: "billing", Secret: "documents-secret"})

	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, minter.prefix)
}

func TestMintModuleRegistrationFailsClosed(t *testing.T) {
	tests := map[string]struct {
		declared string
		prefix   string
		secret   string
	}{
		"wrong secret":      {"documents:" + registrationDigest("right"), "documents", "guessed"},
		"undeclared prefix": {"documents:" + registrationDigest("right"), "unknown", "right"},
		"nothing declared":  {"", "documents", "right"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			minter := installModuleRegistrar(t, test.declared)

			_, err := ModuleCapabilitiesSingleton().MintModuleRegistration(context.Background(),
				&gen.ModuleMintRegistrationRequest{Prefix: test.prefix, Secret: test.secret})

			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Empty(t, minter.prefix)
		})
	}
}

// An unset registrar must deny rather than treat "no policy" as "any policy".
func TestMintModuleRegistrationDeniesWithoutRegistrar(t *testing.T) {
	previous := service
	t.Cleanup(func() { service = previous })
	svc, err := business.NewService(&moduleRegistrationStore{})
	require.NoError(t, err)
	WithService(svc)

	_, err = ModuleCapabilitiesSingleton().MintModuleRegistration(context.Background(),
		&gen.ModuleMintRegistrationRequest{Prefix: "documents", Secret: "documents-secret"})

	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// Malformed input is rejected by request validation before any comparison runs.
func TestMintModuleRegistrationRejectsMalformedRequest(t *testing.T) {
	installModuleRegistrar(t, "documents:"+registrationDigest("right"))

	for name, req := range map[string]*gen.ModuleMintRegistrationRequest{
		"path prefix":     {Prefix: "documents/nested", Secret: "right"},
		"wildcard prefix": {Prefix: "*", Secret: "right"},
		"empty prefix":    {Prefix: "", Secret: "right"},
		"empty secret":    {Prefix: "documents", Secret: ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ModuleCapabilitiesSingleton().MintModuleRegistration(context.Background(), req)
			require.Error(t, err)
		})
	}
}

// A signing failure must not masquerade as an authorization refusal: the module
// should retry, not conclude its secret is wrong.
func TestMintModuleRegistrationSurfacesSigningFailure(t *testing.T) {
	minter := installModuleRegistrar(t, "documents:"+registrationDigest("right"))
	minter.err = errors.New("no signing key")

	_, err := ModuleCapabilitiesSingleton().MintModuleRegistration(context.Background(),
		&gen.ModuleMintRegistrationRequest{Prefix: "documents", Secret: "right"})

	require.Error(t, err)
	require.NotEqual(t, codes.PermissionDenied, status.Code(err))
}

func TestParseRegistrationSecrets(t *testing.T) {
	secrets, err := business.ParseRegistrationSecrets(
		" documents:" + registrationDigest("a") + " , billing:" + registrationDigest("b") + " ,")
	require.NoError(t, err)
	require.Len(t, secrets, 2)
	require.Contains(t, secrets, "documents")
	require.Contains(t, secrets, "billing")

	invalid := map[string]string{
		"missing digest":    "documents",
		"not hex":           "documents:zzzz",
		"wrong digest size": "documents:" + hex.EncodeToString([]byte("short")),
		"path prefix":       "documents/nested:" + registrationDigest("a"),
		"wildcard prefix":   "*:" + registrationDigest("a"),
		"duplicate prefix":  "documents:" + registrationDigest("a") + ",documents:" + registrationDigest("b"),
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := business.ParseRegistrationSecrets(raw)
			require.Error(t, err)
		})
	}
}
