package adapters

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"accounts/pkg/business"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	corework "github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

// TestModuleActorChildPayloadContract pins what a callee may rely on in every
// module_actor child, for each audience a binding may address: the person owns
// it, the calling module is its one hop with exactly the binding's scopes
// (resource ids included), the sealed binding id names that module and that
// binding, and it never outlives the parent or a minute.
func TestModuleActorChildPayloadContract(t *testing.T) {
	source := []*basev0.WorkScopeV1{{ResourceKind: business.SourceOperationResourceKind, Actions: []string{"invoke", "read"}, ResourceIds: []string{actorSource}}}
	sourceLookup := []*basev0.WorkScopeV1{{ResourceKind: business.SourceOperationResourceKind, Actions: []string{"read"}, ResourceIds: []string{actorSource}}}
	for _, arm := range []struct {
		name, binding, audience string
		lookup                  bool
		scopes                  []*basev0.WorkScopeV1
	}{
		{"module", "act", "compute", false, []*basev0.WorkScopeV1{{ResourceKind: "profiles", Actions: []string{"invoke", "read"}}}},
		{"module lookup", "act", "compute", true, []*basev0.WorkScopeV1{{ResourceKind: "profiles", Actions: []string{"read"}}}},
		{"solution", "act-solution", actorSolutionAudience, false, []*basev0.WorkScopeV1{{ResourceKind: "tickets", Actions: []string{"read"}}}},
		{"source", "act-source", business.SourceOperationAudience, false, source},
		{"source lookup", "act-source", business.SourceOperationAudience, true, sourceLookup},
	} {
		t.Run(arm.name, func(t *testing.T) {
			c := moduleActorFixture(t, moduleActorScopes)
			parentClaims, err := corework.Decode(c.parent.Token)
			require.NoError(t, err)
			parent := parentClaims.Context()

			issued, err := c.exchange(arm.binding, c.parent.Token, c.moduleToken, arm.lookup)
			require.NoError(t, err)
			decoded, err := corework.Decode(issued.Token)
			require.NoError(t, err)
			child := decoded.Context()

			require.Equal(t, readOwner, child.OwnerPrincipalId)
			require.Equal(t, business.PrincipalKindHuman, child.GetOwnerPrincipalKind())
			require.Equal(t, parent.TenantId, child.TenantId)
			require.Equal(t, parent.TaskId, child.TaskId)
			require.Equal(t, arm.audience, child.Audience)

			require.Len(t, child.ActorChain, 1)
			hop := child.ActorChain[0]
			require.Equal(t, business.ModulePrincipalID("reader"), hop.PrincipalId)
			require.Equal(t, c.moduleClaims.OwnerPrincipalId, hop.PrincipalId)
			require.Equal(t, business.PrincipalKindService, hop.PrincipalKind)
			require.NotEmpty(t, hop.DelegationId)
			require.Equal(t, hop.PrincipalId, issued.CurrentActorPrincipalId)
			require.Len(t, hop.GrantedScopes, len(arm.scopes))
			for i, want := range arm.scopes {
				got := hop.GrantedScopes[i]
				require.Equal(t, want.ResourceKind, got.ResourceKind)
				require.ElementsMatch(t, want.Actions, got.Actions)
				require.Equal(t, want.ResourceIds, got.ResourceIds)
			}

			id := child.GetOperationBinding().GetBindingId()
			require.True(t, strings.HasPrefix(id, "installed-actor/"), id)
			raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "installed-actor/"))
			require.NoError(t, err)
			var named struct{ Principal, Binding string }
			require.NoError(t, json.Unmarshal(raw, &named))
			require.Equal(t, hop.PrincipalId, named.Principal)
			require.Equal(t, arm.binding, named.Binding)

			require.LessOrEqual(t, child.ExpiresAtUnix, parent.ExpiresAtUnix)
			require.LessOrEqual(t, child.ExpiresAtUnix-child.IssuedAtUnix, int64(60))
		})
	}
}
