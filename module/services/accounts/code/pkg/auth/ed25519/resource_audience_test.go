package ed25519minter_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
)

// A verified token's audience set is read as exactly one of three things:
// bound to nothing, bound to one valid resource, or unreadable. The third is
// REFUSED rather than projected as the first.
//
// Identity.Resource is the scope every caller downstream reads, and an empty
// value there means "no resource binding" — the classification admitted most
// widely. So an audience set this cannot reduce to one valid resource must not
// produce an empty one; it must produce no identity at all.
//
// The gateway holds the identical rule on its own side of the module boundary,
// which it spells separately because nothing crosses that boundary
// (TestTheAudienceClassificationHasThreeOutcomes).
func TestAnUnreadableResourceAudienceIsRefusedNotProjected(t *testing.T) {
	const host = "test-audience"
	const good = "https://h.example.com/api/solutions/example/proxy/mcp"

	for name, audience := range map[string]jwt.ClaimStrings{
		"two resources":  {host, good, "https://h.example.com/api/solutions/other/proxy/mcp"},
		"carries query":  {host, good + "?x=1"},
		"carries anchor": {host, good + "#f"},
		"carries userinfo": {host,
			"https://user@h.example.com/api/solutions/example/proxy/mcp"},
		"not a resource this host issues": {host, "https://h.example.com/elsewhere"},
		"not absolute":                    {host, "/api/solutions/example/proxy/mcp"},
	} {
		t.Run(name, func(t *testing.T) {
			m, priv := minterWithKey(t)
			token := signWithAudience(t, priv, audience)

			identity, err := m.VerifyAccess(token)

			require.ErrorIs(t, err, auth.ErrInvalidResourceAudience)
			require.Nil(t, identity, "no identity may be returned for a scope that cannot be stated")
		})
	}
}

// The two readable shapes still verify, so the refusal above is about the
// audience set and not about resource binding in general.
func TestAReadableResourceAudienceStillVerifies(t *testing.T) {
	const host = "test-audience"
	const good = "https://h.example.com/api/solutions/example/proxy/mcp"

	for name, expect := range map[string]struct {
		audience jwt.ClaimStrings
		resource string
	}{
		"bound to nothing":        {jwt.ClaimStrings{host}, ""},
		"bound to one resource":   {jwt.ClaimStrings{host, good}, good},
		"loopback origin is fine": {jwt.ClaimStrings{host, "http://localhost:3000/api/solutions/example/proxy/mcp"}, "http://localhost:3000/api/solutions/example/proxy/mcp"},
	} {
		t.Run(name, func(t *testing.T) {
			m, priv := minterWithKey(t)
			identity, err := m.VerifyAccess(signWithAudience(t, priv, expect.audience))
			require.NoError(t, err)
			require.Equal(t, expect.resource, identity.Resource)
		})
	}
}

// minterWithKey builds a minter and hands back the key to sign with, so a test
// can present a token shape the minter itself would never produce.
func minterWithKey(t *testing.T) (*ed25519minter.Minter, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	m := ed25519minter.New(ed25519minter.Config{
		Issuer:   "test-issuer",
		Audience: "test-audience",
	}, priv, &memoryStore{})
	return m, priv
}

func signWithAudience(t *testing.T, priv ed25519.PrivateKey, audience jwt.ClaimStrings) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": "test-issuer",
		"aud": []string(audience),
		"sub": uuid.NewString(),
		"org": uuid.NewString(),
		"sid": uuid.NewString(),
		"iat": time.Now().Unix(),
		"nbf": time.Now().Add(-time.Second).Unix(),
		"exp": time.Now().Add(15 * time.Minute).Unix(),
		"jti": "jti",
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(priv)
	require.NoError(t, err)
	return signed
}
