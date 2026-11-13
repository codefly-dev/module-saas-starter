package adapters

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A member declares the operations of a personal source of their own, decided
// by datasource:invoke on its boundary and by no organization role; any other
// source stays an administrator's.
func TestAMemberDeclaresOnTheirOwnPersonalSourceWhereTheyMayCallIt(t *testing.T) {
	org, member, other, source := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
	for _, c := range []struct {
		name    string
		owner   string
		allowed bool
		want    connect.Code
		reason  string
	}{
		{name: "their own, where they may call it", owner: member, allowed: true},
		{name: "their own, where they may not call it", owner: member, want: connect.CodePermissionDenied, reason: "you do not hold datasource:invoke on scope node boundary"},
		{name: "another member's", owner: other, allowed: true, want: connect.CodePermissionDenied, reason: "requires organization administrator or owner role"},
		{name: "the organization's own", allowed: true, want: connect.CodePermissionDenied, reason: "requires organization administrator or owner role"},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := &sourceWireStore{allowed: c.allowed, role: gen.OrgRole_ORG_ROLE_MEMBER,
				source: &business.DatasourceSource{ID: source, OrgID: org, Provider: business.DatasourceProviderAPI, BoundaryNodeID: "boundary", PersonalOwnerUserID: c.owner}}
			svc, err := business.NewService(store)
			require.NoError(t, err)
			previous, cache := service, orgMembershipCache
			service, orgMembershipCache = svc, nil
			t.Cleanup(func() { service, orgMembershipCache = previous, cache })
			ctx := stampVerifiedIdentity(t.Context(), member, org, auth.Assurance{})
			_, err = (&datasourceConnectHandler{svc: svc}).DeclareSourceOperations(ctx, connect.NewRequest(&gen.DeclareSourceOperationsRequest{OrgId: org, SourceId: source}))
			if c.want == 0 {
				require.NoError(t, err)
				return
			}
			require.Equal(t, c.want, connect.CodeOf(err), "%v", err)
			require.Contains(t, err.Error(), c.reason)
		})
	}
}

// A cast reaches a handler past the role-view gate only with a request that can
// be about nothing but a personal source of its person's own; the handler then
// decides whose it is.
func TestACastReachesOnlyRequestsForItsPersonsOwnSource(t *testing.T) {
	const ds = "/saas.accounts.v1.DatasourceService/"
	personal := &gen.AddSourceRequest{Provider: gen.DatasourceProvider_DATASOURCE_PROVIDER_API, Config: &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{
		CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_OAUTH2, Oauth2: &gen.ApiOAuth2Config{Grant: gen.ApiOAuth2Config_GRANT_AUTHORIZATION_CODE}}}}
	governed := &gen.AddSourceRequest{Provider: personal.Provider, Config: personal.Config, Governance: &gen.SourceGovernance{InstallationId: "i", ResourceKind: "k", Action: "write"}}
	clientCredentials := &gen.AddSourceRequest{Provider: personal.Provider, Config: &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{
		CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_OAUTH2, Oauth2: &gen.ApiOAuth2Config{Grant: gen.ApiOAuth2Config_GRANT_CLIENT_CREDENTIALS}}}}
	bearer := &gen.AddSourceRequest{Provider: personal.Provider, Config: &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER}}}
	for _, c := range []struct {
		method string
		req    any
		admit  bool
	}{
		{ds + "AddSource", personal, true},
		{ds + "AddSource", governed, false},
		{ds + "AddSource", clientCredentials, false},
		{ds + "AddSource", bearer, false},
		{ds + "AddSource", &gen.AddSourceRequest{}, false},
		{ds + "AddGitHubSource", &gen.AddGitHubSourceRequest{}, false},
		{ds + "UpdateSource", personal, false},
		{ds + "DeclareSourceOperations", &gen.DeclareSourceOperationsRequest{SourceId: "s"}, true},
		{ds + "DeclareSourceOperations", &gen.DeclareSourceOperationsRequest{}, false},
		{ds + "BeginDatasourceAccountLink", &gen.BeginDatasourceAccountLinkRequest{SourceId: "s"}, true},
		{ds + "BeginDatasourceAccountLink", &gen.BeginDatasourceAccountLinkRequest{Connector: "github"}, false},
		{ds + "CompleteDatasourceAccountLink", &gen.CompleteDatasourceAccountLinkRequest{State: "x"}, true},
		{ds + "CompleteDatasourceAccountLink", &gen.CompleteDatasourceAccountLinkRequest{}, false},
		{ds + "DeleteSource", &gen.DeleteSourceRequest{Id: "s"}, false},
		{ds + "AddSource", nil, false},
	} {
		require.Equal(t, c.admit, castAsksForItsPersonsOwnSource(c.method, c.req), "%s %v", c.method, c.req)
	}
}
