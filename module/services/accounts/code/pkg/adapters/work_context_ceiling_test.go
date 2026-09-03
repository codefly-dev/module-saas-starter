package adapters

import (
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

func TestEnforceActorAudience(t *testing.T) {
	agent := func(auds ...string) *business.Principal {
		return &business.Principal{AgentIdentifier: "pub/agent:1.0.0", AllowedAudiences: auds}
	}

	t.Run("nil actor is unrestricted", func(t *testing.T) {
		require.NoError(t, enforceActorAudience(nil, "anything"))
	})
	t.Run("empty ceiling is unrestricted", func(t *testing.T) {
		require.NoError(t, enforceActorAudience(agent(), "anything"))
	})
	t.Run("audience within ceiling", func(t *testing.T) {
		require.NoError(t, enforceActorAudience(agent("github", "jira"), "jira"))
	})
	t.Run("audience outside ceiling is denied", func(t *testing.T) {
		err := enforceActorAudience(agent("github"), "jira")
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
}

func TestEnforceActorCeiling(t *testing.T) {
	agent := &business.Principal{
		AgentIdentifier:  "pub/agent:1.0.0",
		AllowedAudiences: []string{"github"},
		AllowedScopes:    []string{"repo", "issue"},
	}

	t.Run("audience and scopes within ceiling", func(t *testing.T) {
		require.NoError(t, enforceActorCeiling(agent, "github",
			[]*basev0.WorkScopeV1{scope("repo", "read"), scope("issue", "write")}))
	})
	t.Run("scope outside ceiling is denied", func(t *testing.T) {
		err := enforceActorCeiling(agent, "github",
			[]*basev0.WorkScopeV1{scope("repo", "read"), scope("secret", "read")})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("audience outside ceiling is denied before scopes", func(t *testing.T) {
		err := enforceActorCeiling(agent, "gitlab", []*basev0.WorkScopeV1{scope("repo", "read")})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("empty scope ceiling admits any resource kind", func(t *testing.T) {
		open := &business.Principal{AllowedAudiences: []string{"github"}}
		require.NoError(t, enforceActorCeiling(open, "github",
			[]*basev0.WorkScopeV1{scope("anything", "do")}))
	})
	t.Run("nil actor is unrestricted", func(t *testing.T) {
		require.NoError(t, enforceActorCeiling(nil, "x", []*basev0.WorkScopeV1{scope("y", "z")}))
	})
}
