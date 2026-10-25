package adapters

import (
	"context"
	"maps"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	corework "github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var bindingRunScope = []*gen.WorkContextScope{{ResourceKind: "runs", Actions: []string{"execute"}}}

func (c moduleActorCase) startTaskNaming(bindings ...string) (*gen.IssuedWorkContext, error) {
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	return c.server.StartTask(caller, &gen.StartTaskWorkContextRequest{
		OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader",
		AuthorityScopes: bindingRunScope, OperationBindings: bindings,
	})
}

// A parent that names its module's binding carries that binding's installed
// scopes, resource ids included, without the caller naming them; and the
// module's exchange of that binding redeems it.
func TestStartTaskCarriesANamedBindingAsInstalled(t *testing.T) {
	c := moduleActorFixture(t, moduleActorScopes)
	parent, err := c.startTaskNaming("act-source")
	require.NoError(t, err)
	decoded, err := corework.Decode(parent.Token)
	require.NoError(t, err)
	scopes := wireScopes(decoded.Context().AuthorityScopes)
	require.Len(t, scopes, 2)
	require.Equal(t, "runs", scopes[0].ResourceKind)
	require.Equal(t, business.SourceOperationResourceKind, scopes[1].ResourceKind)
	require.Equal(t, []string{"invoke", "read"}, scopes[1].Actions)
	require.Equal(t, []string{actorSource}, scopes[1].ResourceIds)

	issued, err := c.exchange("act-source", parent.Token, c.moduleToken, false)
	require.NoError(t, err)
	child, err := corework.Decode(issued.Token)
	require.NoError(t, err)
	require.Equal(t, []string{actorSource}, child.Context().ActorChain[0].GrantedScopes[0].ResourceIds)
}

// A caller that also names the binding's scope gets it once.
func TestStartTaskDoesNotRepeatANamedScope(t *testing.T) {
	c := moduleActorFixture(t, moduleActorScopes)
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	explicit := append(append([]*gen.WorkContextScope(nil), bindingRunScope...),
		&gen.WorkContextScope{ResourceKind: business.SourceOperationResourceKind, Actions: []string{"invoke", "read"}, ResourceIds: []string{actorSource}})
	parent, err := c.server.StartTask(caller, &gen.StartTaskWorkContextRequest{
		OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", AuthorityScopes: explicit, OperationBindings: []string{"act-source"},
	})
	require.NoError(t, err)
	decoded, err := corework.Decode(parent.Token)
	require.NoError(t, err)
	require.Len(t, decoded.Context().AuthorityScopes, 2)
}

func TestStartTaskOperationBindingRefusals(t *testing.T) {
	t.Run("a binding the module is not installed with", func(t *testing.T) {
		c := moduleActorFixture(t, moduleActorScopes)
		_, err := c.startTaskNaming("no-such-binding")
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "installed operation audience required")
	})
	t.Run("an installation narrowed away from the binding's sources", func(t *testing.T) {
		c := moduleActorFixture(t, moduleActorScopes)
		live := c.f.live[business.ModulePrincipalID("reader")]
		scope := live.Scope
		scope.Follows = false
		scope.OperationAudiences = maps.Clone(scope.OperationAudiences)
		narrowed := scope.OperationAudiences["act-source"]
		elsewhere := []business.ModuleOperationScope{{ResourceKind: business.SourceOperationResourceKind, Actions: []string{"invoke", "read"}, ResourceIDs: []string{"source-b"}}}
		narrowed.InvokeScopes, narrowed.LookupScopes = elsewhere, elsewhere
		scope.OperationAudiences["act-source"] = narrowed
		live.Scope = scope
		_, err := c.startTaskNaming("act-source")
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "installed operation audience required")
	})
	t.Run("a binding the person holds nothing under, refused by name", func(t *testing.T) {
		c := moduleActorFixture(t, moduleActorScopes)
		c.server.authority = sourceAccessWithdrawn{}
		_, err := c.startTaskNaming("act-source")
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), `"act-source"`)
	})
	t.Run("an actor's mint", func(t *testing.T) {
		c := moduleActorFixture(t, moduleActorScopes)
		caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
		_, err := c.server.StartTask(caller, &gen.StartTaskWorkContextRequest{
			OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", ActorPrincipalId: uuid.NewString(),
			AuthorityScopes: bindingRunScope, OperationBindings: []string{"act-source"},
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}
