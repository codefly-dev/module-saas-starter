package adapters

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// THE SEAL FOLLOWS THE OWNER, pinned over every mint a person's capability comes
// from.
//
// A person's capability is sealed to the organization's installation of the
// audience it addresses; a module or installation principal's is sealed to its
// own installation. The gateway's bearer-less module arm depends on that
// difference: it admits a capability for module X without a bearer only when the
// seal names an active installation OTHER than the organization's installation
// of X, which is what unattended work carries and what a person's capability
// never does. A person mint that let the seal drift to another audience's
// installation would be admitted there without the person's bearer.
//
// Every case below either mints a capability sealed to its audience's
// installation, or is refused. Each parent that is re-derived is sealed to a
// stale installation first, so a mint that copied the parent's seal fails here.
// A new person mint belongs in this table.

// sealPinMint is one person mint: the server it ran on, the audience it asked
// for, and what it answered.
type sealPinMint func(t *testing.T) (server *WorkContextAuthorityServer, audience string, token string, err error)

func TestAPersonsCapabilityIsSealedToItsAudiencesInstallation(t *testing.T) {
	childRequest := func(parent, audience string) *gen.StartChildSessionWorkContextRequest {
		return &gen.StartChildSessionWorkContextRequest{
			OrgId: renewOrgID, ParentWorkContextToken: parent, ActorPrincipalId: renewActorID,
			Audience: audience, TtlSeconds: 60,
			GrantedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
		}
	}
	issued := func(out *gen.IssuedWorkContext, err error) (string, error) {
		return out.GetToken(), err
	}

	for _, tc := range []struct {
		name    string
		refused bool
		mint    sealPinMint
	}{
		{name: "StartTask", mint: func(t *testing.T) (*WorkContextAuthorityServer, string, string, error) {
			exchangeInstallationService(t)
			server := newCoreTestServer(t, &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12}}, newSealAnswersFake(12))
			token, err := issued(server.StartTask(exchangeCaller(), &gen.StartTaskWorkContextRequest{
				OrgId: renewOrgID, TaskId: renewTaskID, Audience: "documents", TtlSeconds: 300,
				AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}},
			}))
			return server, "documents", token, err
		}},
		{name: "StartRootSession", mint: func(t *testing.T) (*WorkContextAuthorityServer, string, string, error) {
			exchangeInstallationService(t)
			server := newRenewTestServer(t)
			parent := personCapability(t, server, "tool.test", "installation-retired")
			token, err := issued(server.StartRootSession(exchangeCaller(), &gen.StartRootSessionWorkContextRequest{
				OrgId: renewOrgID, ParentWorkContextToken: parent, SessionId: renewSession,
				Audience: "documents", TtlSeconds: 300,
			}))
			return server, "documents", token, err
		}},
		{name: "ExchangeAudience", mint: func(t *testing.T) (*WorkContextAuthorityServer, string, string, error) {
			exchangeInstallationService(t)
			server := newRenewTestServer(t)
			parent := personCapability(t, server, "tool.test", "installation-retired")
			token, err := issued(server.ExchangeAudience(exchangeCaller(), &gen.ExchangeWorkContextAudienceRequest{
				OrgId: renewOrgID, ParentWorkContextToken: parent, Audience: "documents", TtlSeconds: 300,
				AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
			}))
			return server, "documents", token, err
		}},
		{name: "RenewWorkContext", mint: func(t *testing.T) (*WorkContextAuthorityServer, string, string, error) {
			exchangeInstallationService(t)
			server := newRenewTestServer(t)
			parent := mintDelegatedContextSealedTo(t, server, "installation-retired")
			token, err := issued(server.RenewWorkContext(exchangeCaller(), &gen.RenewWorkContextRequest{
				OrgId: renewOrgID, ParentWorkContextToken: parent, TtlSeconds: 900,
			}))
			return server, "tool.test", token, err
		}},
		{name: "StartChildSession for the parent's audience", mint: func(t *testing.T) (*WorkContextAuthorityServer, string, string, error) {
			exchangeInstallationService(t)
			server := newRenewTestServer(t)
			parent := personCapability(t, server, "tool.test", testInstallation("tool.test"))
			token, err := issued(server.StartChildSession(exchangeCaller(), childRequest(parent, "tool.test")))
			return server, "tool.test", token, err
		}},
		{name: "StartChildSession for another audience", refused: true, mint: func(t *testing.T) (*WorkContextAuthorityServer, string, string, error) {
			exchangeInstallationService(t)
			server := newRenewTestServer(t)
			parent := personCapability(t, server, "tool.test", testInstallation("tool.test"))
			token, err := issued(server.StartChildSession(exchangeCaller(), childRequest(parent, "documents")))
			return server, "documents", token, err
		}},
		{name: "StartChildSession from a parent sealed to a replaced installation", refused: true, mint: func(t *testing.T) (*WorkContextAuthorityServer, string, string, error) {
			exchangeInstallationService(t)
			server := newRenewTestServer(t)
			parent := personCapability(t, server, "tool.test", "installation-retired")
			token, err := issued(server.StartChildSession(exchangeCaller(), childRequest(parent, "tool.test")))
			return server, "tool.test", token, err
		}},
		{name: "a module's exchange through its operation binding", mint: func(t *testing.T) (*WorkContextAuthorityServer, string, string, error) {
			_, _, client, _ := sourceReadFixture(t)
			service.SetModuleAuthorityReads(currentModuleAuthority{service: service}, nil)
			service.SetModuleCapabilities(nil, nil, installedOperationRegistry(true))
			parent, _ := mintTestCapability(t, workContextSingleton, testCapability{
				Audience: "example", Tenant: readOrg, Owner: readOwner,
				Task: "019f6bf7-1111-7111-8111-111111111111", Session: "019f6bf7-2222-7222-8222-222222222222",
				Scopes:       []*basev0.WorkScopeV1{{ResourceKind: "results", Actions: []string{"read", "write"}, ResourceIds: []string{"result-1"}}},
				Installation: "installation-retired",
			})
			module := mintModuleIdentity(t, business.ModulePrincipalID("example"), readOrg)
			req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "generate", ParentWorkContextToken: parent})
			req.Header().Set(workContextHeader, module)
			req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
			out, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
			if err != nil {
				return workContextSingleton, "example-producer", "", err
			}
			return workContextSingleton, "example-producer", out.Msg.Token, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, audience, token, err := tc.mint(t)
			if tc.refused {
				require.Error(t, err, "a person mint that cannot keep its audience's seal must be refused")
				require.Empty(t, token)
				return
			}
			require.NoError(t, err)
			minted, err := server.verifyIssued(context.Background(), token)
			require.NoError(t, err)
			claims := minted.Context()
			require.Equal(t, business.PrincipalKindHuman, claims.GetOwnerPrincipalKind())
			require.Equal(t, audience, claims.GetAudience())
			require.Equal(t, testInstallation(audience), claims.GetSeal().GetInstallationId(),
				"a person's capability is sealed to its audience's installation, never another's")
		})
	}
}

// The cross-installation case on its own: a person's capability for one audience
// cannot be extended by a child into another audience, because the child would
// keep the first audience's seal. The same-audience child, the hop a runtime
// takes for its own agent, is still minted.
func TestStartChildSessionRefusesAPersonsChildForAnotherAudiencesInstallation(t *testing.T) {
	exchangeInstallationService(t)
	server := newRenewTestServer(t)
	parent := personCapability(t, server, "tool.test", testInstallation("tool.test"))
	ctx := accountsauth.WithVerifiedSessionIDString(exchangeCaller(), renewSession)
	request := func(audience string) *gen.StartChildSessionWorkContextRequest {
		return &gen.StartChildSessionWorkContextRequest{
			OrgId: renewOrgID, ParentWorkContextToken: parent, ActorPrincipalId: renewActorID,
			Audience: audience, TtlSeconds: 60,
			GrantedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
		}
	}

	_, err := server.StartChildSession(ctx, request("documents"))
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.NotEqual(t, testInstallation("tool.test"), testInstallation("documents"),
		"the two audiences must resolve to different installations, or the refusal proves nothing")

	same, err := server.StartChildSession(ctx, request("tool.test"))
	require.NoError(t, err)
	child, err := server.verifyIssued(context.Background(), same.GetToken())
	require.NoError(t, err)
	require.Equal(t, testInstallation("tool.test"), child.Context().GetSeal().GetInstallationId())
}
