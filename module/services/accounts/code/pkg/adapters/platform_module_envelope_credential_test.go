package adapters

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WHO MAY WRITE THE CEILING, AND WITH WHAT EVIDENCE.
//
// The module envelope is the authority ceiling every module's registry entry is
// derived inside, so a SESSION writes it only with fresh AAL2 evidence. An API
// KEY has no second factor and never will — there is no human at it to step up
// — and refusing it outright is what left this ceiling writable from a browser
// and nowhere else, with no operator surface at all. A key is therefore
// admitted exactly when it carries the one scope that names this write.
//
// What keeps that narrow: the scope names this ceiling alone, a key cannot
// mint another key (CreateAPIKey admits only an interactive session), so a
// machine credential can never widen itself into this one, and the key's owner
// must still hold platform super_admin.
func TestModuleEnvelopeWriteEvidenceByCredentialKind(t *testing.T) {
	t.Run("a session with no recent MFA is refused", func(t *testing.T) {
		err := requireModuleEnvelopeWrite(withCredentialKind(context.Background(), credentialKindSession))
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "mfa_required")
	})

	t.Run("a credential the perimeter did not name takes the session rule", func(t *testing.T) {
		err := requireModuleEnvelopeWrite(context.Background())
		require.Equal(t, codes.FailedPrecondition, status.Code(err),
			"an unnamed credential must never fall through to the key path")
	})

	t.Run("a key carrying the write scope is admitted", func(t *testing.T) {
		ctx := withScopes(withCredentialKind(context.Background(), credentialKindAPIKey),
			[]string{moduleEnvelopeWriteScope})
		require.NoError(t, requireModuleEnvelopeWrite(ctx))
	})

	t.Run("a key without that scope is refused", func(t *testing.T) {
		ctx := withScopes(withCredentialKind(context.Background(), credentialKindAPIKey),
			[]string{"users:read", moduleEnvelopeReadScope})
		err := requireModuleEnvelopeWrite(ctx)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), moduleEnvelopeWriteScope)
	})

	t.Run("a key carrying no scope at all is refused", func(t *testing.T) {
		err := requireModuleEnvelopeWrite(withCredentialKind(context.Background(), credentialKindAPIKey))
		require.Equal(t, codes.PermissionDenied, status.Code(err),
			"a key with no scopes is unconstrained in the other direction: it names nothing, so it may do nothing here")
	})

	t.Run("the read scope does not carry the write", func(t *testing.T) {
		ctx := withScopes(withCredentialKind(context.Background(), credentialKindAPIKey),
			[]string{moduleEnvelopeReadScope})
		require.Equal(t, codes.PermissionDenied, status.Code(requireModuleEnvelopeWrite(ctx)))
	})

	// The scope strings are a wire contract: they are minted into a key, joined
	// into X-Scopes by the perimeter and split again here, so neither half may
	// hold a separator.
	t.Run("the two scopes name this ceiling and nothing else", func(t *testing.T) {
		require.Equal(t, "module_envelope:read", moduleEnvelopeReadScope)
		require.Equal(t, "module_envelope:write", moduleEnvelopeWriteScope)
		for _, scope := range []string{moduleEnvelopeReadScope, moduleEnvelopeWriteScope} {
			require.NotContains(t, scope, ",")
			require.Equal(t, 1, strings.Count(scope, ":"))
		}
	})
}

// A wildcard scope is not a grant of this one. The scope that stands in for a
// second factor has to be a scope somebody granted BY NAME: `*:*` is a scope
// real automation keys hold, and admitting the ceiling write on it would admit
// it on a credential nobody minted for this, with no step-up either. Both
// methods follow the same rule, so neither is the soft way in.
func TestAWildcardScopeDoesNotCarryTheEnvelopeScopes(t *testing.T) {
	for _, granted := range []string{"*:*", "*:write", "module_envelope:*", "module_envelope:read"} {
		t.Run(granted, func(t *testing.T) {
			err := requireModuleEnvelopeWrite(withScopes(withCredentialKind(context.Background(), credentialKindAPIKey), []string{granted}))
			require.Error(t, err, "a key holding %q must not write the ceiling", granted)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Contains(t, err.Error(), "must be granted by name")
		})
	}
	require.NoError(t, requireModuleEnvelopeWrite(withScopes(withCredentialKind(context.Background(), credentialKindAPIKey), []string{moduleEnvelopeWriteScope})))

	for _, granted := range []string{"*:*", "*:read", "module_envelope:*", "module_envelope:write"} {
		err := requireModuleEnvelopeRead(withScopes(withCredentialKind(context.Background(), credentialKindAPIKey), []string{granted}))
		require.Error(t, err, "a key holding %q must not read the ceiling", granted)
	}
	require.NoError(t, requireModuleEnvelopeRead(withScopes(withCredentialKind(context.Background(), credentialKindAPIKey), []string{moduleEnvelopeReadScope})))
}

// A session's authority stays the platform role it already had: the read is
// not taken away from a session holding super_admin that happens to present a
// narrow scope list, which is what a plain requireScope would have done.
func TestAScopedSessionKeepsTheEnvelopeRead(t *testing.T) {
	require.NoError(t, requireModuleEnvelopeRead(withScopes(withCredentialKind(context.Background(), credentialKindSession), []string{"users:read"})))
}
