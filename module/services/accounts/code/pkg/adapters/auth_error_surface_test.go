package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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

// A sign-out is acknowledged only when every revocation held. Reporting success
// while the access half failed tells the caller their session ended when it has
// not, which is the one answer a sign-out must never give.
type partiallyRevokingMinter struct {
	auth.JWTMinter
	accessErr  error
	sessionErr error
	familyErr  error
	revoked    []string
}

func (m *partiallyRevokingMinter) RevokeAccess(context.Context, string) error {
	m.revoked = append(m.revoked, "access")
	return m.accessErr
}

func (m *partiallyRevokingMinter) RevokeSessionAccess(context.Context, string) error {
	m.revoked = append(m.revoked, "session")
	return m.sessionErr
}

func (m *partiallyRevokingMinter) Revoke(context.Context, string) error {
	m.revoked = append(m.revoked, "family")
	return m.familyErr
}

func TestR1019LogoutRequiresCompleteRevocation(t *testing.T) {
	boom := errors.New("revocation store unavailable")
	for _, tc := range []struct {
		name       string
		accessErr  error
		sessionErr error
	}{
		{"the access marker fails", boom, nil},
		{"the session marker fails", nil, boom},
		{"both fail", boom, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			minter := &partiallyRevokingMinter{accessErr: tc.accessErr, sessionErr: tc.sessionErr}
			installLogoutMinter(t, minter)

			_, err := (&AuthServer{}).Logout(logoutContext(), &gen.LogoutRequest{RefreshToken: "rt"})

			require.Error(t, err, "an incomplete sign-out must not be reported as success")
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Contains(t, minter.revoked, "family",
				"the durable half must still be attempted and kept")
		})
	}
}

// The healthy control: every revocation succeeds and the caller is told so.
func TestR1019LogoutSucceedsWhenRevocationIsComplete(t *testing.T) {
	minter := &partiallyRevokingMinter{}
	installLogoutMinter(t, minter)

	_, err := (&AuthServer{}).Logout(logoutContext(), &gen.LogoutRequest{RefreshToken: "rt"})

	require.NoError(t, err)
	require.Equal(t, []string{"access", "session", "family"}, minter.revoked)
}

// A failed FAMILY revocation is the durable half failing, and reaches the caller as
// itself rather than as the incomplete-sign-out reason.
func TestR1019LogoutFamilyFailureIsNotMaskedAsIncomplete(t *testing.T) {
	minter := &partiallyRevokingMinter{familyErr: errors.New("session store unavailable")}
	installLogoutMinter(t, minter)

	_, err := (&AuthServer{}).Logout(logoutContext(), &gen.LogoutRequest{RefreshToken: "rt"})

	require.Error(t, err)
	require.NotEqual(t, codes.Unavailable, status.Code(err))
}

func installLogoutMinter(t *testing.T, minter auth.JWTMinter) {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })
	svc, err := business.NewService(&authErrorSurfaceStore{})
	require.NoError(t, err)
	svc.SetJWTMinter(minter)
	WithService(svc)
}

// A caller with a verified session and a bearer, so both access revocations run.
func logoutContext() context.Context {
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer an-access-token"))
	return auth.WithVerifiedSessionID(ctx, logoutSessionID)
}

var logoutSessionID = uuid.MustParse("00000000-0000-4000-8000-0000000000c1")

// SP-GW-13: a failed request carries a stable public reason and the cause is logged,
// not returned. Only the recognized refusals were sanitized, so an injected storage
// failure returned its internal address and wrapped call chain.
func TestR1019RefreshFailureHasPublicReason(t *testing.T) {
	installRefusingRefreshMinter(t, errors.New(
		"dial tcp 10.4.0.9:5432: connect: connection refused (session_store.RotateRefresh)"))

	_, err := (&AuthServer{}).RefreshToken(context.Background(),
		&gen.RefreshTokenRequest{RefreshToken: "a-refresh-token"})

	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err),
		"a host-side failure is retryable, not a credential problem")
	message := status.Convert(err).Message()
	require.Equal(t, "refresh temporarily unavailable", message)
	for _, leak := range []string{"10.4.0.9", "5432", "session_store", "dial tcp", "rpc error"} {
		require.NotContains(t, message, leak,
			"the public reason must name no internal address or call chain")
	}
}

// A deliberate status from further in keeps its own reason, so the mapping above does
// not flatten every answer into one.
func TestR1019RefreshKeepsADeliberateStatus(t *testing.T) {
	installRefusingRefreshMinter(t,
		status.Error(codes.FailedPrecondition, "device session is no longer active"))

	_, err := (&AuthServer{}).RefreshToken(context.Background(),
		&gen.RefreshTokenRequest{RefreshToken: "a-refresh-token"})

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, "device session is no longer active", status.Convert(err).Message())
}
