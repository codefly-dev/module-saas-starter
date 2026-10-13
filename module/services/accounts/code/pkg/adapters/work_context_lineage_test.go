package adapters

import (
	"context"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A capability re-issued for another audience DESCENDS FROM THE ONE PRESENTED:
// its parent_session_id is that capability's session.
//
// Without the link a holder of the parent could not tell a re-issue of its own
// capability from a re-issue of any other capability the same owner holds, and
// the only safe answer available to a callee pairing the two was to refuse
// every one. The re-issue used to be minted afresh from the owner, so it
// descended from an intermediate this host never released — or from nothing at
// all, when the owner acted directly and the mint was a plain session.
//
// These tests state the whole shape, not only the link, because the link is
// worth nothing if anything else about the authority moved with it.

// reissuedFrom is the capability a re-issue of parent must be: the parent field
// for field, except what a new signature for a new callee has to move — the
// audience, key, nonce, window, replay policy, the new session and its lineage
// — and the effective authority when the caller attenuated it.
//
// Of the SEAL only the installation and its revision are allowed to move, and
// only that much is taken from the child: the principal epoch and the owner's
// execution stay the parent's, so a derivation that restamped them from live
// state — which is how a revoked credential would be renewed by deriving —
// fails here. The operation binding is not taken from the child at all, so it
// must be the parent's exactly.
func reissuedFrom(parent, child *basev0.WorkContextV1, attenuated []*basev0.WorkScopeV1) *basev0.WorkContextV1 {
	expected, _ := proto.Clone(parent).(*basev0.WorkContextV1)
	expected.Audience, expected.KeyId, expected.Nonce = child.Audience, child.KeyId, child.Nonce
	expected.NotBeforeUnix, expected.IssuedAtUnix = child.NotBeforeUnix, child.IssuedAtUnix
	expected.ExpiresAtUnix, expected.ReplayPolicy = child.ExpiresAtUnix, child.ReplayPolicy
	expected.SessionId, expected.ParentSessionId = child.SessionId, child.ParentSessionId
	if seal := expected.GetSeal(); seal != nil {
		seal.InstallationId = child.GetSeal().GetInstallationId()
		seal.InstallationRevision = child.GetSeal().GetInstallationRevision()
	}
	if attenuated != nil {
		if hops := expected.GetActorChain(); len(hops) > 0 {
			hops[len(hops)-1].GrantedScopes = attenuated
		} else {
			expected.AuthorityScopes = attenuated
		}
	}
	return expected
}

func requireSameCapability(t *testing.T, want, got *basev0.WorkContextV1) {
	t.Helper()
	if proto.Equal(want, got) {
		return
	}
	require.Fail(t, "the re-issue is not the parent re-pointed at another audience",
		"want:\n%s\ngot:\n%s", want.String(), got.String())
}

func TestAnAudienceExchangeDescendsFromThePresentedParent(t *testing.T) {
	exchangeInstallationService(t)
	server := newRenewTestServer(t)
	parentToken := personCapability(t, server, "tool.test", testInstallation("tool.test"))
	parent, err := server.verifyIssued(context.Background(), parentToken)
	require.NoError(t, err)

	issued, err := server.ExchangeAudience(exchangeCaller(), &gen.ExchangeWorkContextAudienceRequest{
		OrgId: renewOrgID, ParentWorkContextToken: parentToken, Audience: "documents", TtlSeconds: 300,
		AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
	require.NoError(t, err)
	verified, err := server.verifyFor(context.Background(), issued.GetToken(), "documents")
	require.NoError(t, err)
	child := verified.Context()

	// The link, and the fresh session it stands in for: core refuses a derived
	// capability that reuses its parent's session, so equality is not available
	// and lineage is what replaces it.
	require.Equal(t, parent.Context().GetSessionId(), child.GetParentSessionId())
	require.NotEqual(t, parent.Context().GetSessionId(), child.GetSessionId())
	require.NotEmpty(t, child.GetSessionId())
	// Reported on the response too, so a caller correlates without decoding.
	require.Equal(t, parent.Context().GetSessionId(), issued.GetParentSessionId())
	require.Equal(t, child.GetSessionId(), issued.GetSessionId())

	// NO NEW HOP. The owner re-pointing its own capability delegated to nobody,
	// and a chain that grew a link would say otherwise — and would grow one
	// further on every exchange after it.
	require.Empty(t, child.GetActorChain())
	requireSameCapability(t, reissuedFrom(parent.Context(), child,
		[]*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}}), child)
}

// The question a callee actually asks: "is this the capability I admitted, or
// another one of the same owner's?" Two capabilities for one owner, in two
// sessions, must yield children nobody can mistake for each other.
func TestChildrenOfDifferentSessionsAreDistinguishable(t *testing.T) {
	exchangeInstallationService(t)
	server := newRenewTestServer(t)

	exchange := func(parentToken string) *basev0.WorkContextV1 {
		t.Helper()
		issued, err := server.ExchangeAudience(exchangeCaller(), &gen.ExchangeWorkContextAudienceRequest{
			OrgId: renewOrgID, ParentWorkContextToken: parentToken, Audience: "documents", TtlSeconds: 300,
			AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
		})
		require.NoError(t, err)
		verified, err := server.verifyFor(context.Background(), issued.GetToken(), "documents")
		require.NoError(t, err)
		return verified.Context()
	}

	first := personCapability(t, server, "tool.test", testInstallation("tool.test"))
	second, _ := mintTestCapability(t, server, testCapability{
		Audience: "tool.test", Tenant: renewOrgID, Owner: renewActorID, Task: renewTaskID,
		Scopes:       []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
		Installation: testInstallation("tool.test"),
	})
	firstParent, err := server.verifyIssued(context.Background(), first)
	require.NoError(t, err)
	secondParent, err := server.verifyIssued(context.Background(), second)
	require.NoError(t, err)
	require.NotEqual(t, firstParent.Context().GetSessionId(), secondParent.Context().GetSessionId(),
		"the fixture must present two sessions, or this test proves nothing")

	firstChild, secondChild := exchange(first), exchange(second)

	require.Equal(t, firstParent.Context().GetSessionId(), firstChild.GetParentSessionId())
	require.Equal(t, secondParent.Context().GetSessionId(), secondChild.GetParentSessionId())
	require.NotEqual(t, firstChild.GetParentSessionId(), secondChild.GetParentSessionId())
}

func TestAnAudienceExchangeCannotOutliveOrWidenItsParent(t *testing.T) {
	exchangeInstallationService(t)
	server := newRenewTestServer(t)

	t.Run("a window longer than the parent has left is clamped to the parent", func(t *testing.T) {
		parentToken, _ := mintTestCapability(t, server, testCapability{
			Audience: "tool.test", Tenant: renewOrgID, Owner: renewActorID, Task: renewTaskID, Session: renewSession,
			Scopes:       []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
			Installation: testInstallation("tool.test"),
			TTL:          60 * 1e9,
		})
		parent, err := server.verifyIssued(context.Background(), parentToken)
		require.NoError(t, err)

		issued, err := server.ExchangeAudience(exchangeCaller(), &gen.ExchangeWorkContextAudienceRequest{
			OrgId: renewOrgID, ParentWorkContextToken: parentToken, Audience: "documents", TtlSeconds: 900,
			AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
		})
		require.NoError(t, err)
		verified, err := server.verifyFor(context.Background(), issued.GetToken(), "documents")
		require.NoError(t, err)

		require.Equal(t, parent.Context().GetExpiresAtUnix(), verified.Context().GetExpiresAtUnix(),
			"a re-mint from the owner could outlive the parent; a derivation cannot")
	})

	t.Run("an action the parent does not hold is refused", func(t *testing.T) {
		parentToken := personCapability(t, server, "tool.test", testInstallation("tool.test"))

		_, err := server.ExchangeAudience(exchangeCaller(), &gen.ExchangeWorkContextAudienceRequest{
			OrgId: renewOrgID, ParentWorkContextToken: parentToken, Audience: "documents", TtlSeconds: 300,
			AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read", "append"}}},
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), "widens authority")
	})
}

// A renewal is the one re-issue that is NOT a derivation: it extends an
// in-flight capability past the window the presented one has left, which no
// derivation can do. So it carries no lineage back to what was presented, and
// says so rather than naming a session it does not descend from.
func TestARenewalIsNotDerivedFromThePresentedCapability(t *testing.T) {
	exchangeInstallationService(t)
	server := newRenewTestServer(t)
	parentToken := mintDelegatedContext(t, server)
	parent, err := server.verifyIssued(context.Background(), parentToken)
	require.NoError(t, err)

	issued, err := server.RenewWorkContext(exchangeCaller(), &gen.RenewWorkContextRequest{
		OrgId: renewOrgID, ParentWorkContextToken: parentToken, TtlSeconds: 900,
	})
	require.NoError(t, err)
	renewed, err := server.verifyIssued(context.Background(), issued.GetToken())
	require.NoError(t, err)

	require.NotEqual(t, parent.Context().GetSessionId(), renewed.Context().GetParentSessionId(),
		"a renewal is minted afresh from the owner, so it descends from no capability its caller presented")
	require.Greater(t, renewed.Context().GetExpiresAtUnix(), parent.Context().GetExpiresAtUnix(),
		"which is the whole point: it outlives what was presented")
	require.Len(t, renewed.Context().GetActorChain(), len(parent.Context().GetActorChain()),
		"and still appends no hop")
}
