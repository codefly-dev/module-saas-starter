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

// A person's capability is sealed to the organization's installation of the
// AUDIENCE it addresses: that is what an uninstall revokes, and what an
// organization that never installed a module lacks. A capability re-derived for
// another audience, or renewed, is sealed to that audience's installation as it
// stands now — never to whatever installation its parent carried.

// exchangeInstallationService wires a business service answering renewActorID's
// membership and declaring the test audiences.
func exchangeInstallationService(t *testing.T) {
	t.Helper()
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })
}

func exchangeCaller() context.Context {
	return stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
}

// personCapability is a capability renewActorID — a member of renewOrgID —
// holds for audience, sealed to installation.
func personCapability(t *testing.T, server *WorkContextAuthorityServer, audience, installation string) string {
	t.Helper()
	token, _ := mintTestCapability(t, server, testCapability{
		Audience: audience, Tenant: renewOrgID, Owner: renewActorID, Task: renewTaskID, Session: renewSession,
		Scopes:       []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
		Installation: installation,
	})
	return token
}

func TestR1093_AnExchangeIsSealedToItsNewAudiencesInstallation(t *testing.T) {
	exchangeInstallationService(t)
	server := newRenewTestServer(t)
	parent := personCapability(t, server, "tool.test", testInstallation("tool.test"))

	issued, err := server.ExchangeAudience(exchangeCaller(), &gen.ExchangeWorkContextAudienceRequest{
		OrgId: renewOrgID, ParentWorkContextToken: parent, Audience: "documents", TtlSeconds: 300,
		AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
	require.NoError(t, err)
	child, err := server.verifyIssued(context.Background(), issued.GetToken())
	require.NoError(t, err)
	require.Equal(t, "documents", child.Context().GetAudience())
	require.Equal(t, testInstallation("documents"), child.Context().GetSeal().GetInstallationId(),
		"sealed to the installation of the audience it now addresses, not the parent's")
}

func TestR1093_AnExchangeIntoAnAudienceTheOrganizationNeverInstalledIsRefused(t *testing.T) {
	exchangeInstallationService(t)
	server := newRenewTestServer(t)
	parent := personCapability(t, server, "tool.test", testInstallation("tool.test"))
	server.seals.(*sealAnswersFake).uninstalled["documents"] = true

	_, err := server.ExchangeAudience(exchangeCaller(), &gen.ExchangeWorkContextAudienceRequest{
		OrgId: renewOrgID, ParentWorkContextToken: parent, Audience: "documents", TtlSeconds: 300,
		AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "documents",
		"refused because the organization has no installation of the audience, which the refusal names")
}

func TestR1093_ARenewalIsSealedToItsAudiencesCurrentInstallation(t *testing.T) {
	exchangeInstallationService(t)
	server := newRenewTestServer(t)
	parent := mintDelegatedContextSealedTo(t, server, "installation-retired")

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	issued, err := server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId: renewOrgID, ParentWorkContextToken: parent, TtlSeconds: 900,
	})
	require.NoError(t, err)
	renewed, err := server.verifyIssued(context.Background(), issued.GetToken())
	require.NoError(t, err)
	require.Equal(t, testInstallation("tool.test"), renewed.Context().GetSeal().GetInstallationId(),
		"re-resolved from the audience as it stands, not carried from the parent")
}

// mintDelegatedContextSealedTo is mintDelegatedContext sealed to installation.
func mintDelegatedContextSealedTo(t *testing.T, server *WorkContextAuthorityServer, installation string) string {
	t.Helper()
	token, _ := mintTestCapability(t, server, testCapability{
		Audience: "tool.test", Tenant: renewOrgID, Owner: renewOwnerID, Task: renewTaskID, Session: renewSession,
		Scopes: []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"append", "read"}}},
		Actors: []delegationHop{agentHop(renewActorID, "019f6bf7-3333-7333-8333-333333333371",
			&basev0.WorkScopeV1{ResourceKind: "evidence", Actions: []string{"append", "read"}})},
		Installation: installation,
	})
	return token
}

// A module exchanging a person's capability through its operation binding
// re-derives it for the binding's audience, so the child is sealed to that
// audience's installation, not the parent's.
func TestR1093_AModuleExchangeIsSealedToTheBindingAudiencesInstallation(t *testing.T) {
	_, _, client, _ := sourceReadFixture(t)
	service.SetModuleAuthorityReads(currentModuleAuthority{service: service}, nil)
	service.SetModuleCapabilities(nil, nil, installedOperationRegistry(true))
	parent, parentClaims := mintTestCapability(t, workContextSingleton, testCapability{
		Audience: "example", Tenant: readOrg, Owner: readOwner,
		Task: "019f6bf7-1111-7111-8111-111111111111", Session: "019f6bf7-2222-7222-8222-222222222222",
		Scopes: []*basev0.WorkScopeV1{{ResourceKind: "results", Actions: []string{"read", "write"}, ResourceIds: []string{"result-1"}}},
	})
	require.Equal(t, testInstallation("example"), parentClaims.GetSeal().GetInstallationId())

	module := mintModuleIdentity(t, business.ModulePrincipalID("example"), readOrg)
	req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "generate", ParentWorkContextToken: parent})
	req.Header().Set(workContextHeader, module)
	req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
	out, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
	require.NoError(t, err)
	child, err := workContextSingleton.verifyIssued(context.Background(), out.Msg.Token)
	require.NoError(t, err)
	require.Equal(t, "example-producer", child.Context().GetAudience())
	require.Equal(t, testInstallation("example-producer"), child.Context().GetSeal().GetInstallationId(),
		"sealed to the binding audience's installation, not the parent's")
}
