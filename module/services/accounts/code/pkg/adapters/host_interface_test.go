package adapters

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// hostChildFixture is a host whose one composed module, tool.test, runs as a
// workload the cluster attests: the issuer, the interface over it and the
// module's credential.
func hostChildFixture(t *testing.T) (*WorkContextAuthorityServer, *HostAuthorityServer, string) {
	t.Helper()
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
	cluster := executiontest.New()
	svc.SetExecutionBinding(cluster)
	workload := business.ModuleWorkload{Namespace: "example", ServiceAccount: "tool", Container: "app"}
	svc.SetModulePrincipals(business.ModulePrincipalRegistry{
		business.ModulePrincipalID("tool.test"): {Prefix: "tool.test", Workloads: []business.ModuleWorkload{workload}},
	})
	declareTestAudiences(svc)
	credential := cluster.Run(executiontest.Workload{Namespace: workload.Namespace, ServiceAccount: workload.ServiceAccount, Container: workload.Container})
	issuer := newCoreTestServer(t, &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12}}, newSealAnswersFake(12))
	return issuer, NewHostAuthorityServer(issuer, nil), credential
}

// hostChildHop mints a person's single-use capability addressed to tool.test
// and has tool.test hop on it to documents through the interface.
func hostChildHop(t *testing.T, issuer *WorkContextAuthorityServer, server *HostAuthorityServer, credential string) (*authorityv1.IssueResponse, error) {
	t.Helper()
	parent, _ := mintTestCapability(t, issuer, testCapability{
		Audience: "tool.test", Tenant: renewOrgID, Owner: renewActorID, Replay: workcontext.ReplaySingleUse,
		Scopes: []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
	return server.Issue(context.Background(), &authorityv1.IssueRequest{
		As:    &authorityv1.IssueRequest_ParentToken{ParentToken: parent},
		Actor: &authorityv1.WorkloadCredential{Credential: credential}, Audience: "documents",
		Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
}

func TestHostChildIssueBindsParentToAuthenticatedActor(t *testing.T) {
	issuer, server, credential := hostChildFixture(t)
	for _, audience := range []string{"tool.test", "documents"} {
		t.Run(audience, func(t *testing.T) {
			parent, _ := mintTestCapability(t, issuer, testCapability{
				Audience: audience, Tenant: renewOrgID, Owner: renewActorID,
				Replay: workcontext.ReplaySingleUse,
				Scopes: []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
			})
			out, err := server.Issue(context.Background(), &authorityv1.IssueRequest{
				As:    &authorityv1.IssueRequest_ParentToken{ParentToken: parent},
				Actor: &authorityv1.WorkloadCredential{Credential: credential}, Audience: "documents",
				Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
			})
			if audience == "tool.test" {
				require.NoError(t, err)
				require.Equal(t, renewActorID, out.GetContext().GetOwnerPrincipalId())
				require.Equal(t, "documents", out.GetContext().GetAudience())
				require.Len(t, out.GetContext().GetActorChain(), 1)
				require.Equal(t, business.ModulePrincipalID("tool.test"), out.GetContext().GetActorChain()[0].GetPrincipalId())
				return
			}
			require.True(t, out == nil, "an unrelated actor must receive no issued capability")
			// Refused by name, as the interface states it: a module hops only on
			// a token addressed to it.
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "hops only on a token addressed to it")
			// An unrelated actor must not consume another audience's single-use parent.
			_, err = issuer.verifyFor(context.Background(), parent, audience)
			require.NoError(t, err)
		})
	}
}

// A check naming a method holds it to the audience's delivered method policy.
// No module has one on this host yet (gap 4), so the interface's rule for that
// case applies: refused by name, or reported beside the verified capability
// when advisory. A capability that does not verify is refused either way.
func TestHostCheckAppliesTheMethodPolicyOrAdvisesIt(t *testing.T) {
	issuer, server, _ := hostChildFixture(t)
	token, _ := mintTestCapability(t, issuer, testCapability{
		Audience: "documents", Tenant: renewOrgID, Owner: renewActorID,
		Scopes: []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
	ctx := context.Background()

	out, err := server.Check(ctx, &authorityv1.CheckRequest{Token: token, Audience: "documents"})
	require.NoError(t, err)
	require.Empty(t, out.GetRefusal())

	const method = "/example.v1.RecordService/GetRecord"
	_, err = server.Check(ctx, &authorityv1.CheckRequest{Token: token, Audience: "documents", Method: method})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), method)

	out, err = server.Check(ctx, &authorityv1.CheckRequest{Token: token, Audience: "documents", Method: method, Advisory: true})
	require.NoError(t, err)
	require.Contains(t, out.GetRefusal(), method)
	require.Equal(t, renewActorID, out.GetContext().GetOwnerPrincipalId(), "advisory answers the verified capability")

	_, err = server.Check(ctx, &authorityv1.CheckRequest{Token: token, Audience: "model", Method: method, Advisory: true})
	require.Equal(t, codes.Unauthenticated, status.Code(err), "advisory never passes a capability addressed elsewhere")
}

// Current answers only a session: a call with no verified identity, or one
// whose credential names no session (an API key, a Work Context), is not one.
func TestHostCurrentRefusesWhatIsNotASession(t *testing.T) {
	server := NewHostSessionServer(nil)
	_, err := server.Current(context.Background(), &sessionv1.CurrentRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	sessionless := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	_, err = server.Current(sessionless, &sessionv1.CurrentRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// The candidates of an empty ask are every action this host names on the kind
// at the audience, from the organisation's roles and the composition's
// bindings to that audience; wildcards and other audiences' bindings add none.
func TestEmptyAskCandidatesAreTheActionsTheHostNames(t *testing.T) {
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
	svc.SetModulePrincipals(business.ModulePrincipalRegistry{
		business.ModulePrincipalID("reader"): {Prefix: "reader", OperationAudiences: map[string]business.ModuleOperationAudience{
			"records":   {Audience: "records", InvokeScopes: []business.ModuleOperationScope{{ResourceKind: "example-records", Actions: []string{"invoke", "read"}}}},
			"elsewhere": {Audience: "model", InvokeScopes: []business.ModuleOperationScope{{ResourceKind: "example-records", Actions: []string{"train"}}}},
		}},
	})
	roles := []*gen.Role{
		{Permissions: []*gen.Permission{{Resource: "example-records", Action: "write"}, {Resource: "example-records", Action: "*"}, {Resource: "*", Action: "delete"}}},
		{Permissions: []*gen.Permission{{Resource: "other", Action: "export"}}},
	}
	require.Equal(t, []string{"invoke", "read", "write"}, candidateActions("example-records", "records", roles))
	require.Empty(t, candidateActions("unnamed", "records", roles))
}

// The interface's installation names its target as the composition does — the
// route alias a frontend binds to ("notes") — never this host's presence row id;
// a row whose presence is gone keeps the id rather than an empty target.
func TestInstallationWireNamesItsTargetByName(t *testing.T) {
	row := &gen.Installation{Id: "i", OrgId: "o", TargetId: "76f50dc4-6209-4a04-ae77-27eff13c110e", Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE}
	if got := installationToWire(row, "notes").GetTarget(); got != "notes" {
		t.Fatalf("target = %q, want the route alias", got)
	}
	if got := installationToWire(row, "").GetTarget(); got != row.TargetId {
		t.Fatalf("target = %q, want the presence id when no alias is known", got)
	}
}

// Only an issue through a binding the acting module declares (a workload's, or
// a child's with the workload as actor) may leave its audience to the binding;
// every other shape that names no audience is refused by name.
func TestAnIssueWithNoAudienceIsAWorkloadThroughABinding(t *testing.T) {
	for name, req := range map[string]*authorityv1.IssueRequest{
		"person":               {As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, OperationBindingId: "documents"},
		"workload, no binding": {As: &authorityv1.IssueRequest_Workload{Workload: &authorityv1.WorkloadCredential{Credential: "x"}}},
		"child":                {As: &authorityv1.IssueRequest_ParentToken{ParentToken: "p"}, OperationBindingId: "documents"},
	} {
		_, err := bindingAudience(context.Background(), req)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "names its audience") {
			t.Errorf("%s: %v, want the audience refused by name", name, err)
		}
	}
}

// An envelope write refused for want of the policy log's witness tells the
// writer why, as FailedPrecondition, instead of an internal error.
func TestAnUnwitnessedEnvelopeNarrowingIsRefusedByName(t *testing.T) {
	err := moduleEnvelopeError(fmt.Errorf("cannot write the module envelope: %w", business.ErrPolicyLogUnreachable), "cannot write the module envelope")
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "policy log") {
		t.Fatalf("got %v, want FailedPrecondition naming the policy log", err)
	}
}
