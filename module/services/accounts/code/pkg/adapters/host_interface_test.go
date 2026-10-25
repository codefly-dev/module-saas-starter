package adapters

import (
	"context"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
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
			require.Equal(t, codes.Unauthenticated, status.Code(err))
			// An unrelated actor must not consume another audience's single-use parent.
			_, err = issuer.verifyFor(context.Background(), parent, audience)
			require.NoError(t, err)
		})
	}
}
