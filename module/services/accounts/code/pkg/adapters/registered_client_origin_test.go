//go:build pure

package adapters

import (
	"context"
	"net/http"
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// The gateway→accounts origin contract, held from the accounts side.
//
// The invariant: the public origin this service treats as verified is fixed by
// operator configuration, and a forwarded value is honoured only where it equals
// the configured one. That guard is right and stays. What this test pins is the
// OTHER half of the same contract — that a registered client calling from its own
// permitted origin is not refused by it.
//
// The two halves met in one header. The gateway stamped a registered client's
// CORS origin as `X-Codefly-Public-Origin`, this service required that value to
// equal the configured host origin, and so configuring the host origin — which
// deployed boot requires — refused every authenticated request from every
// registered browser client before its RPC ran. Neither guard was wrong; the
// header meant two things about two different parties.
//
// It is asserted by VALUE rather than by driving the gateway, because the gateway
// is a separate Go module with no `go.work` and an accounts test cannot import it.
// The gateway's own half (`TestCORS_ARegisteredClientDoesNotSpeakForTheHostOrigin`)
// asserts that it stamps no public origin for a registered client; this asserts
// that what it does stamp is admitted. Together they are the contract; apart,
// either could pass while the pair stayed broken, which is exactly what happened.
func TestRegisteredClientReachesAccountsUnderAPinnedHostOrigin(t *testing.T) {
	const (
		configuredHostOrigin = "https://host.example"
		registeredClient     = "example-addin"
		clientOrigin         = "https://addin.example"
	)

	previousToken := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousToken) })

	previousOrigin, _ := auth.ConfiguredPublicOrigin()
	require.NoError(t, auth.SetConfiguredPublicOrigin(configuredHostOrigin))
	t.Cleanup(func() { _ = auth.SetConfiguredPublicOrigin(previousOrigin) })

	// What the gateway forwards for an authenticated, registered browser client:
	// the identity it verified, its own trust credential, and the client's id —
	// and, since the separation, NO public origin, because the client is not a
	// witness to this host's.
	forwarded := func() http.Header {
		h := http.Header{}
		h.Set("X-Codefly-Gateway-Token", "test-gateway-token")
		h.Set("X-User-Id", "01a12b95-153e-7c35-977b-7e1f6cffea65")
		h.Set("X-Org-Id", "01a12b95-153e-7c38-b042-e09562cfd4f3")
		h.Set("X-Org-Role", "admin")
		h.Set("X-Session-Id", "01a12b95-153e-7c3c-86d2-80e898a68c54")
		h.Set("X-Credential-Kind", "session")
		// Present and empty, exactly as the gateway sends it. The forwarded
		// credential is incomplete without this header at all, which is a
		// different refusal from the one under test.
		h.Set("X-Scopes", "")
		h.Set("X-Client-Id", registeredClient)
		h.Set("Origin", clientOrigin)
		return h
	}

	authorize := func(t *testing.T, transport string, h http.Header) error {
		t.Helper()
		if transport == "connect" {
			_, err := (&connectPolicyInterceptor{}).authorize(
				context.Background(), "/saas.accounts.v1.UserService/GetSelf", h)
			return err
		}
		md := metadata.MD{}
		for name, values := range h {
			md.Set(name, values...)
		}
		_, err := (&grpcPolicyAuthorizer{exposure: rpcExposureTenant}).authorize(
			metadata.NewIncomingContext(context.Background(), md),
			"/saas.accounts.v1.UserService/GetSelf")
		return err
	}

	for _, transport := range []string{"connect", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			// The host's own frontend path: it proves the configured origin and is
			// admitted. This is the control — it passed before and must still.
			host := forwarded()
			host.Set(publicOriginHeader, configuredHostOrigin)
			require.NoError(t, authorize(t, transport, host),
				"the host's own origin must stay admitted")

			// The registered client, on its own origin, carrying no claim about
			// this host's. Admitted, which is the defect this closes.
			require.NoError(t, authorize(t, transport, forwarded()),
				"a registered client on its permitted origin must reach accounts")

			// And the guard still bites: a forwarded origin that is NOT the
			// configured one is refused, whoever sent it. Were this to pass, the
			// separation above would have been bought by dropping the protection.
			foreign := forwarded()
			foreign.Set(publicOriginHeader, clientOrigin)
			require.Error(t, authorize(t, transport, foreign),
				"an origin other than the configured one must stay refused")
		})
	}
}
