package adapters

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Issuing a credential is a privilege-changing operation, so it sits behind the
// same second factor as every other one (SP-IDENT-10).
//
// CreateAPIKey was the hole: it ran requireInteractiveSession, requireScope,
// requireScopesWithinCaller and requireOrgAdmin, and then minted. An
// organization owner with a verified session and NO enrolled factor could ask
// for `*:*` and get a credential that authenticates as them for as long as it
// lives, without the session that asked for it and across a password change —
// which is the thing a factor on the privileged mutations is protecting.
//
// Asserted from the source, in the shape that catches the two ways this breaks:
// deleting the call, and calling it with its error discarded.
func TestCredentialIssuanceRequiresAFactor(t *testing.T) {
	text := executableSource(t, "rpcs.go")

	body := regexp.MustCompile(
		`func \(s \*APIKeyServer\) CreateAPIKey\((?s:.*?)\n\}\n`).FindString(text)
	require.NotEmpty(t, body, "CreateAPIKey must still be here to be gated")

	require.Regexp(t, guardReturnsItsRefusal(`requireMFA\(ctx, actorID\)`), body,
		"credential issuance must require a recent factor AND return that refusal")

	// Ordering: after the authorization guards, before the mutation. A factor
	// check the write has already passed is not a check.
	gate := regexp.MustCompile(`requireMFA\(ctx, actorID\)`).FindStringIndex(body)
	mint := strings.Index(body, "service.CreateAPIKey(")
	admin := strings.Index(body, "requireOrgAdmin(")
	require.NotEqual(t, -1, mint)
	require.NotEqual(t, -1, admin)
	require.Less(t, admin, gate[0], "the factor runs after the authorization guards")
	require.Less(t, gate[0], mint, "and before the key is minted")
}

// The declaration and the enforcement must agree. The gate above holds the
// handler; this holds the POLICY, which is what the authorization-coverage gate
// and the generated gateway catalog read. CreateAPIKey declared
// MFA_REQUIREMENT_NONE while asserting a factor in code would have left the two
// describing different hosts.
func TestCredentialIssuanceDeclaresTheFactorItEnforces(t *testing.T) {
	proto, err := os.ReadFile("../../../proto/saas/accounts/v1/api_keys.proto")
	require.NoError(t, err)

	rpc := regexp.MustCompile(
		`rpc CreateAPIKey\((?s:.*?)\n  \}\n`).FindString(string(proto))
	require.NotEmpty(t, rpc)
	require.Contains(t, rpc, "mfa: MFA_REQUIREMENT_IF_ENROLLED_RECENT_STEP_UP",
		"the declared policy must require the factor the handler enforces")
	require.NotContains(t, rpc, "mfa: MFA_REQUIREMENT_NONE")
}
