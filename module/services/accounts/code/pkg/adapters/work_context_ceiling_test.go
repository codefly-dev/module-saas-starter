package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"

	"accounts/pkg/business"
)

func scope(kind string, actions ...string) *basev0.WorkScopeV1 {
	return &basev0.WorkScopeV1{ResourceKind: kind, Actions: actions}
}

// The ceiling is read through the host's CURRENT vocabulary (issue #952), so these
// audiences are the real shapes — `solution:<binding-id>` — rather than the free
// text `allowed_audiences` used to admit.
func TestEnforceActorAudience(t *testing.T) {
	const (
		one = "solution:acme.test.one"
		two = "solution:acme.test.two"
	)
	agent := func(auds ...string) *business.Principal {
		return &business.Principal{AgentIdentifier: "pub/agent:1.0.0", AllowedAudiences: auds}
	}
	ctx := context.Background()

	t.Run("nil actor is unrestricted", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one")
		require.NoError(t, enforceActorAudience(ctx, nil, one))
	})
	t.Run("empty ceiling is unrestricted", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one")
		require.NoError(t, enforceActorAudience(ctx, agent(), one))
	})
	t.Run("audience within ceiling", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one", "acme.test.two")
		require.NoError(t, enforceActorAudience(ctx, agent(one, two), two))
	})
	t.Run("audience outside ceiling is denied", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one", "acme.test.two")
		err := enforceActorAudience(ctx, agent(one), two)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	// THE READ-TIME HALF. A ceiling entry that has left the host's vocabulary is
	// DEAD, not granted: the solution it named has been withdrawn, so it describes
	// no consumer. Write-time validation cannot cover this, because the set shrinks
	// by DELIVERY, long after any write.
	t.Run("a withdrawn solution's audience is no longer granted", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one") // two has been withdrawn
		require.NoError(t, enforceActorAudience(ctx, agent(one, two), one),
			"the live entry must still be granted, or a withdrawal would revoke the whole ceiling")
		err := enforceActorAudience(ctx, agent(one, two), two)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), "still serves",
			"the refusal must say the audience is no longer served, not that it was never allowed")
	})

	// A ceiling that has gone ENTIRELY dead refuses, and is not read as an empty
	// ceiling. An empty stored list means "unrestricted"; a list that was non-empty
	// and is now wholly withdrawn means "every consumer this agent could address is
	// gone". Collapsing the second into the first is the most permissive reading of
	// the most restrictive state.
	t.Run("a wholly withdrawn ceiling refuses rather than reading as unrestricted", func(t *testing.T) {
		withHostAudiences(t) // no declared bindings at all
		err := enforceActorAudience(ctx, agent(one, two), one)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), "withdrawn")
	})

	// And a vocabulary that cannot be READ fails closed: a ceiling nobody can
	// resolve is not an unrestricted one.
	t.Run("an unreadable vocabulary fails the ceiling closed", func(t *testing.T) {
		store := withHostAudiences(t, "acme.test.one")
		store.err = context.DeadlineExceeded
		err := enforceActorAudience(ctx, agent(one), one)
		require.Equal(t, codes.Unavailable, status.Code(err))
	})
}

func TestEnforceActorCeiling(t *testing.T) {
	const live = "solution:acme.test.one"
	agent := &business.Principal{
		AgentIdentifier:  "pub/agent:1.0.0",
		AllowedAudiences: []string{live},
		AllowedScopes:    []string{"repo", "issue"},
	}
	ctx := context.Background()

	t.Run("audience and scopes within ceiling", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one")
		require.NoError(t, enforceActorCeiling(ctx, agent, live,
			[]*basev0.WorkScopeV1{scope("repo", "read"), scope("issue", "write")}))
	})
	t.Run("scope outside ceiling is denied", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one")
		err := enforceActorCeiling(ctx, agent, live,
			[]*basev0.WorkScopeV1{scope("repo", "read"), scope("secret", "read")})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("audience outside ceiling is denied before scopes", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one", "acme.test.two")
		err := enforceActorCeiling(ctx, agent, "solution:acme.test.two", []*basev0.WorkScopeV1{scope("repo", "read")})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("empty scope ceiling admits any resource kind", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one")
		open := &business.Principal{AllowedAudiences: []string{live}}
		require.NoError(t, enforceActorCeiling(ctx, open, live,
			[]*basev0.WorkScopeV1{scope("anything", "do")}))
	})
	t.Run("nil actor is unrestricted", func(t *testing.T) {
		withHostAudiences(t, "acme.test.one")
		require.NoError(t, enforceActorCeiling(ctx, nil, live, []*basev0.WorkScopeV1{scope("y", "z")}))
	})
}
