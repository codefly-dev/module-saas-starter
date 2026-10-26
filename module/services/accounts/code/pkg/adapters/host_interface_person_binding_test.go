package adapters

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHostPersonIssueCarriesItsNamedBinding(t *testing.T) {
	c := moduleActorFixture(t, moduleActorScopes)
	server := NewHostAuthorityServer(c.server, nil)
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	request := &authorityv1.IssueRequest{
		As:       &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}},
		Audience: "reader", OperationBindingId: "act-source",
	}
	for _, operation := range []string{"", "invoke"} {
		request.Operation = operation
		out, err := server.Issue(caller, request)
		require.NoError(t, err)
		wc := claims(t, out)
		require.Equal(t, readOwner, wc.GetOwnerPrincipalId())
		require.Len(t, wc.GetAuthorityScopes(), 1)
		scope := wc.GetAuthorityScopes()[0]
		require.Equal(t, business.SourceOperationResourceKind, scope.GetResourceKind())
		require.Equal(t, []string{"invoke", "read"}, scope.GetActions())
		require.Equal(t, []string{actorSource}, scope.GetResourceIds())
		_, err = c.exchange("act-source", out.GetToken(), c.moduleToken, false)
		require.NoError(t, err, "the module redeems the person's installed binding")
	}
	request.Operation = "lookup"
	_, err := server.Issue(caller, request)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "act-source")
	require.Contains(t, status.Convert(err).Message(), "lookup")

	// At a solution whose roles decide, the binding carries what those roles
	// grant within it; roles that grant nothing are refused naming the binding.
	request.Operation = ""
	server.roles = fixedSolutionRoles{"reader": {}}
	_, err = server.Issue(caller, request)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), `holds nothing at`)
	require.Contains(t, status.Convert(err).Message(), "act-source")
	server.roles = nil

	c.server.authority = sourceAccessWithdrawn{}
	_, err = server.Issue(caller, request)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "act-source")
}

func TestStartTaskRefusesAnEmptyAuthorityAsk(t *testing.T) {
	c := moduleActorFixture(t, moduleActorScopes)
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	_, err := c.server.StartTask(caller, &gen.StartTaskWorkContextRequest{
		OrgId: readOrg, TaskId: readOrg, Audience: "reader",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "authority_scopes or operation_bindings")
}
