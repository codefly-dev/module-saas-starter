package oidc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"accounts/pkg/auth/oidc"

	"github.com/stretchr/testify/require"
)

func TestDiscoveryURLDerivation(t *testing.T) {
	// The spec appends the well-known path to the issuer, preserving any path
	// the issuer already carries. WorkOS scopes its issuer to the application,
	// so dropping that path would fetch the wrong document.
	got, err := oidc.DiscoveryURL("https://api.workos.com/user_management/client_123")
	require.NoError(t, err)
	require.Equal(t,
		"https://api.workos.com/user_management/client_123/.well-known/openid-configuration",
		got)

	got, err = oidc.DiscoveryURL("https://tenant.example.com/")
	require.NoError(t, err)
	require.Equal(t, "https://tenant.example.com/.well-known/openid-configuration", got)

	// An issuer already pointing at the document is accepted unchanged, so an
	// operator may configure either form.
	already := "https://tenant.example.com/.well-known/openid-configuration"
	got, err = oidc.DiscoveryURL(already)
	require.NoError(t, err)
	require.Equal(t, already, got)
}

func TestDiscoveryURLRejectsUnusableIssuers(t *testing.T) {
	for _, issuer := range []string{"", "   ", "not-a-url", "ftp://example.com"} {
		_, err := oidc.DiscoveryURL(issuer)
		require.Error(t, err, "issuer %q must be rejected", issuer)
	}

	// Plaintext HTTP is only tolerable against loopback, where there is no
	// network to intercept.
	_, err := oidc.DiscoveryURL("http://identity.example.com")
	require.Error(t, err)
	_, err = oidc.DiscoveryURL("http://localhost:8080")
	require.NoError(t, err)
}

func TestDiscoverReadsProviderMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/openid-configuration", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer": "http://` + r.Host + `",
			"authorization_endpoint": "http://` + r.Host + `/authorize",
			"token_endpoint": "http://` + r.Host + `/token",
			"jwks_uri": "http://` + r.Host + `/jwks"
		}`))
	}))
	defer server.Close()

	discovered, err := oidc.Discover(context.Background(), server.URL, server.Client())
	require.NoError(t, err)
	require.Equal(t, server.URL, discovered.Issuer)
	require.Equal(t, server.URL+"/token", discovered.TokenEndpoint)
	require.Equal(t, server.URL+"/jwks", discovered.JWKSURI)
}

func TestDiscoverRejectsIncompleteMetadata(t *testing.T) {
	// A document without an issuer or key set cannot configure a validator, and
	// failing at startup is far better than accepting tokens later.
	for _, body := range []string{
		`{"jwks_uri": "https://example.com/jwks"}`,
		`{"issuer": "https://example.com"}`,
		`not json`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := oidc.Discover(context.Background(), server.URL, server.Client())
		require.Error(t, err, "metadata %q must be rejected", body)
		server.Close()
	}
}

func TestDiscoverSurfacesTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := oidc.Discover(context.Background(), server.URL, server.Client())
	require.ErrorContains(t, err, "http 404")
}
