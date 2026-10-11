package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"
	"github.com/stretchr/testify/require"
)

type installationTransport struct {
	calls  int
	bearer string
}

func (s *installationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls++
	s.bearer = r.Header.Get("Authorization")
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("installed")), Request: r}, nil
}

// Exercise the real authentication, resolution and admission path without a
// listener. Only the upstream transport and accounts RPC are test doubles.
func TestDynamicModuleProxyInstallationAndAttribution(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authz := &ExtAuthz{keys: staticAccessKeys(pub), issuer: gatewayTokenIssuer(), publicBase: publicBaseURL(), acceptedIssuers: gatewayAcceptedIssuers(), audience: "saas-starter", revoker: noopRevoker{}}
	registry := newFakeSolutionRegistry()
	registry.records["example-module"] = &accountsv1.SolutionRegistration{
		SolutionId: "example-module", Publisher: "solution:example-module", Revision: 1,
		Declared: &accountsv1.SolutionDeclaredBinding{BindingId: "acme.test.example", TargetId: fakeSolutionTarget("example-module"), Generation: 1, Release: "acme/example@1.0.0", Kind: accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE},
		Backend:  &accountsv1.SolutionBackendBinding{Revision: 1, Upstream: "http://127.0.0.1:18080", ServiceAlias: "example-module", ContractVersion: "v1"},
	}
	gw := NewGateway(authz, NewRouteMatcher(testRouteEntries(), nil), nil, nil, registry, newFakeClientRegistry())
	require.NoError(t, gw.solutions.refresh(context.Background()))
	authority := &entitledTo{}
	gw.solutionEntitlements = authority
	transport := &installationTransport{}
	gw.registeredTransport = transport
	token := signValidToken(t, priv)
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/example-module/records", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Org-Id", "forged-org")
		r.Header.Set("X-User-Id", "forged-viewer")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, http.StatusForbidden, request().Code)
	require.Zero(t, transport.calls, "no consent means no bearer reaches the module")
	require.Len(t, authority.calls, 1)
	require.NotEmpty(t, authority.calls[0].OrgId)
	require.NotEqual(t, "forged-org", authority.calls[0].OrgId)
	require.NotEqual(t, "forged-viewer", authority.calls[0].SubjectId)
	authority.ids = []string{fakeSolutionTarget("example-module")}
	require.Equal(t, http.StatusOK, request().Code)
	require.Equal(t, 1, transport.calls)
	require.Equal(t, "Bearer "+token, transport.bearer)
	// No admission cache: withdrawal is observed on the very next request.
	authority.ids = nil
	require.Equal(t, http.StatusForbidden, request().Code)
	require.Equal(t, 1, transport.calls)
	require.Len(t, authority.calls, 3)
	require.Equal(t, authority.calls[0].OrgId, authority.calls[2].OrgId)
	gw.solutionEntitlements = nil
	require.Equal(t, http.StatusServiceUnavailable, request().Code)
	require.Equal(t, 1, transport.calls)
}
