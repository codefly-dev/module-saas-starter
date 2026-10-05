package business

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

// The bindings a presented authorization code must satisfy, as a decision
// rather than as a side effect of a database transaction — so the rules are
// held in a suite that needs no database.
//
// A1007-05 is the rule this exists for: redemption holds the presented redirect
// URI against BOTH the code row and the client's policy as it stands NOW. A
// metadata client has no registry row to delete, so re-publishing its document
// without a callback is how it withdraws one; honouring the code
// row alone kept accepting one the publisher had taken down.

const redemptionVerifier = "ZmFrZS12ZXJpZmllci10aGF0LWlzLWxvbmctZW5vdWdoLTAxMjM0NTY3ODk"

func redemptionChallenge() string {
	digest := sha256.Sum256([]byte(redemptionVerifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func metadataClientAllowing(redirectURIs ...string) auth.RegisteredClient {
	return auth.RegisteredClient{
		ClientID:     "https://claude.ai/oauth/claude-code-client-metadata",
		Name:         "Claude Code",
		Origin:       "https://claude.ai",
		RedirectURIs: redirectURIs,
		Metadata:     true,
	}
}

func issuedCode(client auth.RegisteredClient, redirectURI string) *ClientAuthorizationCode {
	return &ClientAuthorizationCode{
		ClientID:      client.ClientID,
		RedirectURI:   redirectURI,
		CodeChallenge: redemptionChallenge(),
	}
}

func presented(redirectURI string) authorizationCodeRedemption {
	return authorizationCodeRedemption{
		RedirectURI:  redirectURI,
		CodeVerifier: redemptionVerifier,
	}
}

func TestEveryRedemptionBindingMustHold(t *testing.T) {
	const callback = "http://localhost/callback"
	client := metadataClientAllowing(callback)

	require.True(t, redemptionBindingsHold(
		client, issuedCode(client, callback), presented(callback)))

	// RFC 8252 §7.3: the registered loopback URI names no port, so the code may
	// have been issued against an ephemeral one and still redeem.
	ephemeral := "http://localhost:54321/callback"
	require.True(t, redemptionBindingsHold(
		client, issuedCode(client, ephemeral), presented(ephemeral)))

	t.Run("a different client", func(t *testing.T) {
		code := issuedCode(client, callback)
		code.ClientID = "https://elsewhere.example.com/doc"
		require.False(t, redemptionBindingsHold(client, code, presented(callback)))
	})

	t.Run("a redirect URI the code was not issued for", func(t *testing.T) {
		require.False(t, redemptionBindingsHold(
			client, issuedCode(client, callback), presented(ephemeral)))
	})

	t.Run("the wrong PKCE verifier", func(t *testing.T) {
		wrong := presented(callback)
		wrong.CodeVerifier = "ZmFrZS12ZXJpZmllci10aGF0LWlzLWxvbmctZW5vdWdoLTk4NzY1NDMyMTA"
		require.False(t, redemptionBindingsHold(
			client, issuedCode(client, callback), wrong))
	})

	// A1007-05. The code's own bindings all hold — same client, same URI, right
	// verifier — and redemption must STILL be refused, because the client's
	// document no longer lists that callback.
	t.Run("a callback the document has since withdrawn", func(t *testing.T) {
		code := issuedCode(client, callback)
		withdrawn := metadataClientAllowing("https://claude.ai/oauth/callback")
		require.False(t, redemptionBindingsHold(withdrawn, code, presented(callback)),
			"a publisher withdraws a callback by removing it from its document; "+
				"there is no row to delete, so redemption is where it must take effect")
	})

	// And an operator-declared client is unaffected: its registry is startup
	// configuration, so its policy cannot change under a live code.
	t.Run("an operator-declared client is unchanged", func(t *testing.T) {
		declared := auth.RegisteredClient{
			ClientID:     "example-addin",
			RedirectURIs: []string{"https://addin.example.com/auth/callback"},
		}
		uri := "https://addin.example.com/auth/callback"
		require.True(t, redemptionBindingsHold(
			declared, issuedCode(declared, uri), presented(uri)))
	})
}
