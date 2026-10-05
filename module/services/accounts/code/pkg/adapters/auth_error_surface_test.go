package adapters

import (
	"context"
	"strings"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A refused refresh is the caller's credential being spent, not this host
// failing. The register's reproducer is a garbage refresh cookie: the session
// store answers ErrRefreshRevoked for every rejection it can reach (unknown
// token, revoked family, expired or idled-out session), the business layer
// wrapped it, and the wrapped chain left the adapter as an unmapped error — so
// the edge answered HTTP 500 carrying the internal call path, and the single
// most ordinary thing a browser can present counted as a server error.
type refusingRefreshMinter struct {
	auth.JWTMinter
	err error
}

func (m *refusingRefreshMinter) VerifyRefresh(context.Context, string) (*auth.TokenPair, error) {
	return nil, m.err
}

type authErrorSurfaceStore struct{ business.Store }

func installRefusingRefreshMinter(t *testing.T, err error) {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })

	svc, svcErr := business.NewService(&authErrorSurfaceStore{})
	require.NoError(t, svcErr)
	svc.SetJWTMinter(&refusingRefreshMinter{err: err})
	WithService(svc)
}

func TestRefreshRefusalIsUnauthenticatedWithNoInternalDetail(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"an unknown or revoked token", auth.ErrRefreshRevoked},
		{"a reused token", auth.ErrRefreshReuse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installRefusingRefreshMinter(t, tc.err)

			resp, err := (&AuthServer{}).RefreshToken(context.Background(),
				&gen.RefreshTokenRequest{RefreshToken: "not-a-refresh-token"})

			require.Nil(t, resp)
			require.Equal(t, codes.Unauthenticated, status.Code(err),
				"a refused refresh must not read as a server error")
			message := status.Convert(err).Message()
			require.Equal(t, "invalid refresh token", message)
			for _, leak := range []string{"rpc error", "RefreshToken", "verify refresh", "ed25519minter", "auth:"} {
				require.NotContains(t, message, leak,
					"the public reason must name no internal component or error chain")
			}
		})
	}
}

// The counterpart: something that is NOT a refusal still reaches the caller as
// itself, so the mapping above cannot be satisfied by turning every refresh
// failure into a credential problem and sending an operator to the wrong half.
func TestRefreshInfrastructureFailureIsNotReportedAsACredentialProblem(t *testing.T) {
	installRefusingRefreshMinter(t, auth.ErrSessionUnavailable)

	_, err := (&AuthServer{}).RefreshToken(context.Background(),
		&gen.RefreshTokenRequest{RefreshToken: "not-a-refresh-token"})

	require.Error(t, err)
	require.NotEqual(t, codes.Unauthenticated, status.Code(err))
}

// BeginOAuth is reached by an unauthenticated caller, so its refusal answers a
// stable public reason. It echoed the wrapped internal chain, which named the
// call path; the probe that found it read `rpc error` back out of the body.
func TestBeginOAuthRefusalNamesNoInternalDetail(t *testing.T) {
	previous := service
	t.Cleanup(func() { service = previous })
	svc, err := business.NewService(&authErrorSurfaceStore{})
	require.NoError(t, err)
	WithService(svc)

	resp, err := (&AuthServer{}).BeginOAuth(context.Background(), &gen.BeginOAuthRequest{
		Provider:    "oidc",
		RedirectUri: "https://evil.example/auth/callback",
	})

	require.Nil(t, resp)
	require.Error(t, err)
	message := status.Convert(err).Message()
	for _, leak := range []string{"rpc error", "cannot mint oauth state", "BeginOAuth", "oauth-state"} {
		require.NotContains(t, message, leak,
			"the public reason must name no internal component or error chain")
	}
	require.False(t, strings.Contains(message, "auth: "),
		"the sentinel's own text is internal detail")
}
