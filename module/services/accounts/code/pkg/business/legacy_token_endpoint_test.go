package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/business"
)

// countingFetcher records whether a metadata document was fetched at all.
type countingFetcher struct{ calls int }

func (f *countingFetcher) Fetch(_ context.Context, _ string) ([]byte, time.Duration, error) {
	f.calls++
	return nil, 0, auth.ErrClientMetadataUnreachable
}

// r1/f6. The legacy registered-client token RPC resolves from the registry only,
// and must not perform an outbound fetch for a caller-supplied URL.
//
// Its paired issue path is registry-only, so no metadata-document client can
// obtain a code through this flow, and the standard endpoints resolve their own
// client without coming through here. Resolving documents here would therefore
// add an unauthenticated outbound fetch — before any code is examined — to a
// published RPC, for a capability nothing can reach.
//
// The assertion is the fetch COUNT, not the refusal: the refusal is the same
// either way, which is exactly why the capability could be added without any
// test noticing.
func TestTheLegacyTokenEndpointNeverFetchesAMetadataDocument(t *testing.T) {
	service, err := business.NewService(nil)
	require.NoError(t, err)

	registry, err := auth.NewClientRegistry(`[{
		"client_id": "example-cli", "name": "Example CLI",
		"redirect_uris": ["http://127.0.0.1/callback"]
	}]`)
	require.NoError(t, err)
	service.SetClientRegistry(registry)

	// A policy that WOULD admit any document, over a fetcher that records the
	// attempt — so a fetch here is visible rather than merely refused.
	fetcher := &countingFetcher{}
	policy, err := auth.NewClientMetadataPolicy("any")
	require.NoError(t, err)
	service.SetClientMetadataResolver(auth.NewClientMetadataResolverWith(policy, fetcher))

	// Non-nil so the call reaches client resolution rather than stopping at the
	// minter guard above it.
	_, priv, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	service.SetJWTMinter(ed25519minter.New(ed25519minter.Config{}, priv, nil))

	_, err = service.ExchangeClientToken(context.Background(), &gen.ExchangeClientTokenRequest{
		ClientId: "https://client.example.com/metadata",
		Grant: &gen.ExchangeClientTokenRequest_AuthorizationCode{
			AuthorizationCode: &gen.AuthorizationCodeGrant{
				Code:         "whatever",
				RedirectUri:  "http://127.0.0.1:4000/callback",
				CodeVerifier: "verifier",
			},
		},
	})

	require.ErrorIs(t, err, auth.ErrClientAuthorizationRejected)
	require.Zero(t, fetcher.calls,
		"the legacy token endpoint must not fetch a document for a caller-supplied URL")
}
