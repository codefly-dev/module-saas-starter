package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// consent is what the person agreed to at connect time; live is what the
// module's binding asks for now. The decision is about one question — does the
// binding ask for MORE than was agreed? — and the digest alone could not ask it.
func TestABindingNarrowingIsHonouredAndAWideningRevokes(t *testing.T) {
	consented := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read", "write"}}}
	binding := func(scopes ...ModuleOperationScope) ModuleOperationAudience {
		return ModuleOperationAudience{
			Audience:               "runtime",
			InvokeScopes:           scopes,
			SourceDelegationScopes: scopes,
		}
	}
	grant := func(b ModuleOperationAudience) ModulePrincipalGrant {
		return ModulePrincipalGrant{Prefix: "documents", OperationAudiences: map[string]ModuleOperationAudience{"runtime-admission": b}}
	}
	recorded := binding(consented...)
	delegation := &SourceDelegation{
		BindingID: "runtime-admission", BindingDigest: SourceDelegationBindingDigest(recorded),
		ConsentedAudience: recorded.Audience, ConsentedScopes: cloneOperationScopes(recorded.SourceDelegationScopes),
	}

	// Unchanged: the digest still matches.
	_, state := grant(recorded).currentSourceDelegationBinding(delegation)
	require.Equal(t, sourceDelegationBindingOK, state)

	// Narrowed: honoured, at the narrower LIVE binding.
	narrowed := binding(ModuleOperationScope{ResourceKind: "collections", Actions: []string{"read"}})
	live, state := grant(narrowed).currentSourceDelegationBinding(delegation)
	require.Equal(t, sourceDelegationBindingOK, state, "strictly less authority never needs consent")
	require.Equal(t, []string{"read"}, live.SourceDelegationScopes[0].Actions)

	// Widened by an action: revoked. (Actions are sorted: the validator
	// requires it, and an unsorted set reads as an unusable binding, not a wider one.)
	wider := binding(ModuleOperationScope{ResourceKind: "collections", Actions: []string{"execute", "read", "write"}})
	_, state = grant(wider).currentSourceDelegationBinding(delegation)
	require.Equal(t, sourceDelegationBindingChanged, state)

	// Widened by a kind: revoked.
	another := binding(consented[0], ModuleOperationScope{ResourceKind: "reports", Actions: []string{"write"}})
	_, state = grant(another).currentSourceDelegationBinding(delegation)
	require.Equal(t, sourceDelegationBindingChanged, state)

	// A different audience is a different delegation, whatever the scopes say.
	elsewhere := binding(consented...)
	elsewhere.Audience = "elsewhere"
	_, state = grant(elsewhere).currentSourceDelegationBinding(delegation)
	require.Equal(t, sourceDelegationBindingChanged, state)

	// Recorded before consent was kept: the digest rule it was written under.
	legacy := &SourceDelegation{BindingID: "runtime-admission", BindingDigest: SourceDelegationBindingDigest(recorded)}
	_, state = grant(narrowed).currentSourceDelegationBinding(legacy)
	require.Equal(t, sourceDelegationBindingChanged, state, "no recorded consent to compare against, so nothing is guessed")
}
