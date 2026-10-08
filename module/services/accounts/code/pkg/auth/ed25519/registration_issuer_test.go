package ed25519minter_test

import (
	"context"
	"testing"

	ed25519minter "accounts/pkg/auth/ed25519"

	"github.com/stretchr/testify/require"
)

// A1007B-02. The module- and solution-registration credentials keep their own
// `iss`, independent of the access-token issuer.
//
// They are internal cluster credentials with their own audiences and their own
// verifiers — one in the gateway, one in the frontend — each pinned to the
// literal `saas-starter`. Access tokens had to move to an https URL because
// RFC 8414 requires the published OAuth issuer to be one; registration
// credentials publish nothing, so moving them with the access token bought
// nothing and refused every verifier that had not moved in the same release.
//
// The failure that caused was asymmetric and therefore quiet: the gateway
// accepted the new token, the frontend did not, so a solution's backend half
// registered while its frontend half was refused — and an active registration
// needs both, so the solution simply never became routable.
func TestRegistrationCredentialsKeepTheLiteralIssuer(t *testing.T) {
	// A deployment that HAS moved its access-token issuer to its public URL.
	const publicIssuer = "https://host.example.com"
	_, priv, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	store := &memoryStore{}
	m := ed25519minter.New(ed25519minter.Config{
		Issuer:   publicIssuer,
		Audience: "saas-starter",
	}, priv, store)

	solutionToken, _, err := m.MintSolutionRegistration("example")
	require.NoError(t, err)
	require.Contains(t, decodeJWTPayload(t, solutionToken), `"iss":"saas-starter"`)
	require.NotContains(t, decodeJWTPayload(t, solutionToken), publicIssuer)

	moduleToken, _, err := m.MintModuleRegistration("example")
	require.NoError(t, err)
	require.Contains(t, decodeJWTPayload(t, moduleToken), `"iss":"saas-starter"`)
	require.NotContains(t, decodeJWTPayload(t, moduleToken), publicIssuer)

	// And the access token in the same deployment DOES carry the URL, so this
	// is a separation rather than a refusal to move anything.
	pair, err := m.Mint(context.Background(), newIdentity())
	require.NoError(t, err)
	require.Contains(t, decodeJWTPayload(t, pair.AccessToken),
		`"iss":"`+publicIssuer+`"`)
}

// The verifier on the other side of that contract is GONE, and so is this
// check.
//
// It read `frontend/code/src/solutions/registration-authority.ts` and required
// its `ISSUER` literal to equal what MintSolutionRegistration signs — because
// the frontend is TypeScript and this is Go, so nothing but a check like that
// kept the two equal. This branch deletes that verifier: the frontend
// registration path is part of the writer surface runtime self-registration
// owns, and the contract now has one side.
//
// It is NOT replaced by a weaker version. A check that the Go literal equals
// itself would pass forever and assert nothing. If a declared-presence
// equivalent of that cross-language contract appears, it needs its own check
// against whatever the new consumer reads — and the shape to copy is the one
// deleted here: read the OTHER language's source and compare the literal.
