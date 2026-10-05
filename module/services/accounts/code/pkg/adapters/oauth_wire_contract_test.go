package adapters

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The frontend↔accounts wire contract for the OAuth surface.
//
// A1007-01. The invariant: the names this handler serialises are the names the
// browser decodes, and neither side may assert the other's shape without
// checking it.
//
// Accounts serialises OAuth snake_case (`requires_consent`); the browser's
// interface is camelCase. The two spellings are different strings, so a cast
// across the seam type-checks while producing `undefined` for every field, and
// an undefined boolean is falsy — a value that cannot be distinguished from a
// real answer. A unit test on either side cannot catch that, because each
// side's test fixes the shape that side expects; so this one is spelled against
// the OTHER side's source.
//
// No unit suite could see it, because each side mocked the other's shape. These
// tests read the REAL serialised response and the REAL decoder source, so the
// seam has something holding it from both ends. The TypeScript half of the same
// contract is in
// frontend/code/src/features/auth/model/__tests__/wire-contract.test.ts, which
// decodes bytes produced by this handler's own field names.

// oauthValidateResponseKeys is the key set the frontend decoder reads. Pinned
// here as a literal rather than derived, so a rename on either side has to be
// made in two places that are checked against each other.
var oauthValidateResponseKeys = []string{
	"client_name",
	"client_origin",
	"client_source",
	"requires_consent",
	"scope",
	"issuer",
}

func TestTheValidateResponseCarriesExactlyTheKeysTheFrontendDecodes(t *testing.T) {
	withGatewayToken(t)
	handler := oauthHandler(t, "any")

	req := httptest.NewRequest(http.MethodPost, OAuthAuthorizeValidatePath,
		strings.NewReader(`{"response_type":"code","client_id":"example-addin","redirect_uri":"https://addin.example.com/auth/callback","code_challenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM","code_challenge_method":"S256"}`))
	req.Header.Set("Content-Type", "application/json")
	trustedOrigin(req, "https://host.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wire))
	for _, key := range oauthValidateResponseKeys {
		require.Contains(t, wire, key,
			"the frontend decoder reads %q; serialising it under another name makes "+
				"that field read as absent, and for requires_consent absent means "+
				"consent is skipped", key)
	}
	// The one that decides whether a person is asked must be a real boolean, not
	// a string or a number that a decoder would have to coerce.
	require.IsType(t, false, wire["requires_consent"])
}

// The decoder on the other side must read those same names. Checked against the
// TypeScript source, because the alternative is trusting that two languages
// agree about a string neither compiler compares.
func TestTheFrontendDecoderReadsTheKeysThisHandlerSerialises(t *testing.T) {
	source := frontendOAuthModelSource(t)
	for _, key := range oauthValidateResponseKeys {
		require.Contains(t, source, "wire."+key,
			"the frontend decoder must read %q off the wire object; a camelCase "+
				"property name silently yields undefined", key)
	}
	// And it must not go back to casting. A cast is what made the mismatch
	// invisible: `as OAuthAuthorizationResolution` type-checks against JSON
	// whose keys are all different.
	require.NotRegexp(t,
		regexp.MustCompile(`as\s+\|?\s*OAuthAuthorizationResolution`), source,
		"the validate response must be decoded, never cast")
}

// Every `json:"..."` name this file's responses declare, as a guard against a
// new field being added in camelCase by habit.
func TestTheOAuthWireNamesAreSnakeCase(t *testing.T) {
	source, err := os.ReadFile("oauth_http.go")
	require.NoError(t, err)
	tags := regexp.MustCompile(`json:"([a-zA-Z_]+)`).FindAllStringSubmatch(string(source), -1)
	require.NotEmpty(t, tags)
	for _, tag := range tags {
		require.NotRegexp(t, regexp.MustCompile(`[a-z][A-Z]`), tag[1],
			"wire name %q is camelCase; this surface is OAuth, which is snake_case, "+
				"and the frontend decoder reads snake_case", tag[1])
	}
}

func frontendOAuthModelSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "frontend", "code", "src",
		"features", "auth", "model", "oauth-authorization.ts")
	source, err := os.ReadFile(path)
	require.NoError(t, err, "the frontend decoder must exist at %s; if it moved, "+
		"move this check with it rather than deleting it", path)
	return string(source)
}
