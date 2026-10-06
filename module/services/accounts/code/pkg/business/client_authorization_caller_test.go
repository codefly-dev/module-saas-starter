package business

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

// Each of the four conditions under which a caller may NOT hand a client a
// credential, decided separately.
//
// They were previously reachable only through the code-issuing path, which
// needs a database — so one test stood for four rules, and because any of them
// produces the same error, a test that stopped at the first refusal it met
// proved nothing about the other three. Removing any one of these guards now
// fails exactly one case here.
func TestWhoMayAuthorizeAClient(t *testing.T) {
	person := uuid.New()
	session := uuid.New()

	t.Run("a signed-in person, as themselves, may", func(t *testing.T) {
		require.NoError(t, callerMayAuthorizeAClient(auth.RequestIdentity{
			EffectiveSubject: person,
			SessionID:        session,
		}, true))
	})

	t.Run("no verified identity at all may not", func(t *testing.T) {
		// The interceptor would normally have stopped this; a rule this
		// consequential does not delegate.
		require.ErrorIs(t, callerMayAuthorizeAClient(auth.RequestIdentity{}, false),
			auth.ErrClientAuthorizationRejected)
	})

	t.Run("a complete-looking identity that was not verified may not", func(t *testing.T) {
		// The case that makes the "not verified" check load-bearing rather than
		// a restatement of the two below: every field is populated, so only the
		// verification flag can refuse it. Written this way deliberately — with
		// a ZERO identity the next condition catches it anyway, so a test using
		// one cannot tell whether this guard exists.
		require.ErrorIs(t, callerMayAuthorizeAClient(auth.RequestIdentity{
			EffectiveSubject: person,
			SessionID:        session,
		}, false), auth.ErrClientAuthorizationRejected)
	})

	t.Run("a verified request naming no person may not", func(t *testing.T) {
		// Verified, but there is nobody for the client to act as.
		require.ErrorIs(t, callerMayAuthorizeAClient(auth.RequestIdentity{
			SessionID: session,
		}, true), auth.ErrClientAuthorizationRejected)
	})

	t.Run("a person with no session may not", func(t *testing.T) {
		// The credential is session-bound for its whole life — that is what
		// revoking a session revokes — so one minted outside a session could
		// never be withdrawn.
		require.ErrorIs(t, callerMayAuthorizeAClient(auth.RequestIdentity{
			EffectiveSubject: person,
		}, true), auth.ErrClientAuthorizationRejected)
	})

	t.Run("an impersonated session may not", func(t *testing.T) {
		// The client would hold a rotatable token for a person who never
		// authorized it, and would keep it after the impersonation ended.
		impersonated := auth.RequestIdentity{
			EffectiveSubject: person,
			SessionID:        session,
			RealActor:        uuid.New(),
		}
		require.True(t, impersonated.Impersonated(), "the fixture must actually be impersonated")
		require.ErrorIs(t, callerMayAuthorizeAClient(impersonated, true),
			auth.ErrClientAuthorizationRejected)
	})
}
