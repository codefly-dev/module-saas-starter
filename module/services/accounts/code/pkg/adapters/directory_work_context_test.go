package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// The double is below the real handlers and business reads. It deliberately
// makes the owner a member of both organizations: membership alone must never
// let a capability for one organization read the other one's directory.
type directoryContextStore struct {
	business.Store
	member bool
	reads  int
}

func (s *directoryContextStore) GetOrgMembership(_ context.Context, org, owner string) (*gen.OrgMembership, error) {
	if !s.member {
		return nil, nil
	}
	return &gen.OrgMembership{OrgId: org, UserId: owner, Role: gen.OrgRole_ORG_ROLE_MEMBER}, nil
}

func (s *directoryContextStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *directoryContextStore) WithOrgTx(ctx context.Context, org string, fn func(context.Context) error) error {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, readOwner); err != nil {
		return err
	}
	return fn(ctx)
}

func (s *directoryContextStore) GetTeamOrgID(_ context.Context, team string) (string, error) {
	if team == sourceB {
		return foreignOrgID, nil
	}
	return readOrg, nil
}

func (s *directoryContextStore) ListOrgMembers(_ context.Context, org string) ([]*gen.OrgMembership, error) {
	s.reads++
	return []*gen.OrgMembership{{OrgId: org, UserId: readOwner}}, nil
}

func (s *directoryContextStore) ListTeams(_ context.Context, org, member string) ([]*gen.Team, error) {
	s.reads++
	return []*gen.Team{{Id: sourceA, OrgId: org, Name: "Example Team"}}, nil
}

func (s *directoryContextStore) ListTeamMembers(context.Context, string) ([]*gen.TeamMembership, error) {
	s.reads++
	return []*gen.TeamMembership{{TeamId: sourceA, UserId: readOwner}}, nil
}

func directoryContextFixture(t *testing.T) (*directoryContextStore, *workContextAuthorityFake, *sealAnswersFake, accountsv1connect.DirectoryServiceClient) {
	t.Helper()
	previousService, previousAuthority, previousCache := service, workContextSingleton, orgMembershipCache
	t.Cleanup(func() {
		service, workContextSingleton, orgMembershipCache = previousService, previousAuthority, previousCache
	})
	orgMembershipCache = nil
	store := &directoryContextStore{member: true}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
	facts := &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 7}}
	seals := newSealAnswersFake(7)
	workContextSingleton = newCoreTestServer(t, facts, seals)
	minter := &fixedAccessMinter{identity: &auth.Identity{UserID: uuid.MustParse(readOwner), OrgID: uuid.MustParse(readOrg)}}
	mux := http.NewServeMux()
	path, handler := accountsv1connect.NewDirectoryServiceHandler(&directoryConnectHandler{inner: &DirectoryServer{}},
		connect.WithInterceptors(connectAuthInterceptor(func() auth.JWTMinter { return minter })))
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return store, facts, seals, accountsv1connect.NewDirectoryServiceClient(server.Client(), server.URL)
}

func callDirectory(t *testing.T, client accountsv1connect.DirectoryServiceClient, token, bearer, org, team string) []error {
	t.Helper()
	members := connect.NewRequest(&gen.ListOrgMembersRequest{OrgId: org})
	teams := connect.NewRequest(&gen.ListTeamsRequest{OrgId: org})
	roster := connect.NewRequest(&gen.ListTeamMembersRequest{TeamId: team})
	for _, req := range []connect.AnyRequest{members, teams, roster} {
		// Caller-controlled identity never overrides either verified carrier.
		req.Header().Set("X-Org-Id", foreignOrgID)
		req.Header().Set("X-User-Id", targetAdminID)
		if token != "" {
			req.Header().Set(workContextHeader, token)
		}
		if bearer != "" {
			req.Header().Set("Authorization", "Bearer "+bearer)
		}
	}
	membersOut, membersErr := client.ListOrganizationMembers(t.Context(), members)
	teamsOut, teamsErr := client.ListTeams(t.Context(), teams)
	rosterOut, rosterErr := client.ListTeamMembers(t.Context(), roster)
	if membersErr == nil {
		require.Equal(t, org, membersOut.Msg.Members[0].OrgId)
		require.Equal(t, readOwner, membersOut.Msg.Members[0].UserId)
	}
	if teamsErr == nil {
		require.Equal(t, org, teamsOut.Msg.Teams[0].OrgId)
	}
	if rosterErr == nil {
		require.Equal(t, readOwner, rosterOut.Msg.Members[0].UserId)
	}
	return []error{membersErr, teamsErr, rosterErr}
}

func TestDirectoryBearerAdmissionUnchanged(t *testing.T) {
	store, _, _, client := directoryContextFixture(t)
	for _, err := range callDirectory(t, client, "", "session", readOrg, sourceA) {
		require.NoError(t, err)
	}
	require.Equal(t, 3, store.reads)
	for _, err := range callDirectory(t, client, "", "", readOrg, sourceA) {
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		require.ErrorContains(t, err, "authentication required")
	}
	for _, err := range callDirectory(t, client, "", "session", foreignOrgID, sourceB) {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.ErrorContains(t, err, "outside the authenticated scope")
	}
	require.Equal(t, 3, store.reads)
}

func TestDirectoryWorkContextAdmission(t *testing.T) {
	store, _, seals, client := directoryContextFixture(t)
	token, _ := mintTestCapability(t, workContextSingleton, testCapability{
		Audience: business.DirectoryAudience, Tenant: readOrg, Owner: readOwner,
	})
	for _, err := range callDirectory(t, client, token, "", readOrg, sourceA) {
		require.NoError(t, err)
	}
	require.Equal(t, 3, store.reads)
	for _, tenant := range seals.askedTenants() {
		require.Equal(t, readOrg, tenant)
	}
	for _, err := range callDirectory(t, client, token, "", foreignOrgID, sourceB) {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.ErrorContains(t, err, "outside the authenticated scope")
	}
	require.Equal(t, 3, store.reads, "a foreign organization must never reach a directory query")
}

func TestDirectoryWorkContextRefusals(t *testing.T) {
	for _, scenario := range []string{"expired", "forged", "wrong audience", "removed member", "revision revoked", "installation revoked", "authority revoked", "single use", "unconfigured"} {
		t.Run(scenario, func(t *testing.T) {
			store, facts, seals, client := directoryContextFixture(t)
			capability := testCapability{Audience: business.DirectoryAudience, Tenant: readOrg, Owner: readOwner}
			if scenario == "wrong audience" {
				capability.Audience = business.SourceOperationAudience
			}
			if scenario == "single use" {
				capability.Replay = workcontext.ReplaySingleUse
			}
			token, claims := mintTestCapability(t, workContextSingleton, capability)
			code, reason := connect.CodeUnauthenticated, ""
			switch scenario {
			case "expired":
				workContextSingleton.now = func() time.Time { return time.Unix(claims.ExpiresAtUnix, 0).Add(time.Minute) }
				reason = "expired at"
			case "forged":
				forger := newCoreTestServer(t, facts, seals)
				token, _ = mintTestCapability(t, forger, capability)
				reason = "signature does not verify"
			case "wrong audience":
				reason = "minted for audience"
			case "removed member":
				store.member = false
				code, reason = connect.CodePermissionDenied, "not a member of this organization"
			case "revision revoked":
				seals.setRevision(8)
				reason = "authorization revision superseded"
			case "installation revoked":
				seals.unheld[claims.GetSeal().GetInstallationId()] = true
				reason = "does not hold"
			case "authority revoked":
				facts.err = business.NewStoreError(errors.New("current organization membership required"), business.ErrTypePermission)
				code, reason = connect.CodePermissionDenied, "current organization membership required"
			case "single use":
				code, reason = connect.CodePermissionDenied, "idempotent directory Work Context required"
			case "unconfigured":
				workContextSingleton = &WorkContextAuthorityServer{}
				code, reason = connect.CodeUnavailable, "directory authority unavailable"
			}
			// A valid bearer cannot hide a refused forwarded capability.
			if scenario == "single use" {
				// The first verification consumes a single-use token; assert the
				// directory's policy refusal before replay becomes the reason.
				request := connect.NewRequest(&gen.ListOrgMembersRequest{OrgId: readOrg})
				request.Header().Set(workContextHeader, token)
				_, err := client.ListOrganizationMembers(t.Context(), request)
				require.Equal(t, code, connect.CodeOf(err))
				require.ErrorContains(t, err, reason)
			} else {
				for _, err := range callDirectory(t, client, token, "session", readOrg, sourceA) {
					require.Equal(t, code, connect.CodeOf(err))
					require.ErrorContains(t, err, reason)
				}
			}
			require.Zero(t, store.reads)
		})
	}
}

func TestDirectoryAmbiguousWorkContextRefused(t *testing.T) {
	store, _, _, client := directoryContextFixture(t)
	token, _ := mintTestCapability(t, workContextSingleton, testCapability{
		Audience: business.DirectoryAudience, Tenant: readOrg, Owner: readOwner,
	})
	request := connect.NewRequest(&gen.ListOrgMembersRequest{OrgId: readOrg})
	request.Header().Add(workContextHeader, token)
	request.Header().Add(workContextHeader, token)
	request.Header().Set("Authorization", "Bearer session")
	_, err := client.ListOrganizationMembers(t.Context(), request)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.ErrorContains(t, err, "one directory Work Context required")
	require.Zero(t, store.reads)
}

func TestDirectoryDelegatedWorkContextRevocation(t *testing.T) {
	store, facts, _, client := directoryContextFixture(t)
	actor := uuid.NewString()
	facts.facts.Actor = &business.Principal{ID: actor}
	journal := &readExchangeJournal{}
	workContextSingleton.journal = journal
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}}}
	token, _ := mintTestCapability(t, workContextSingleton, testCapability{
		Audience: business.DirectoryAudience, Tenant: readOrg, Owner: readOwner,
		Scopes: scopes, Actors: []delegationHop{agentHop(actor, uuid.NewString(), scopes...)},
	})
	for _, err := range callDirectory(t, client, token, "", readOrg, sourceA) {
		require.NoError(t, err)
	}
	require.NotEmpty(t, journal.ids)
	journal.revoked = journal.ids[0]
	for _, err := range callDirectory(t, client, token, "", readOrg, sourceA) {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.ErrorContains(t, err, "actor chain hop has been revoked")
	}
	workContextSingleton.journal = nil
	for _, err := range callDirectory(t, client, token, "", readOrg, sourceA) {
		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		require.ErrorContains(t, err, "delegation journal unavailable")
	}
	require.Equal(t, 3, store.reads)
}

func (f *installedReadStore) ListOrgMembers(ctx context.Context, org string) ([]*gen.OrgMembership, error) {
	return f.directory.ListOrgMembers(ctx, org)
}
func (f *installedReadStore) ListTeams(ctx context.Context, org, member string) ([]*gen.Team, error) {
	return f.directory.ListTeams(ctx, org, member)
}
func (f *installedReadStore) ListTeamMembers(ctx context.Context, team string) ([]*gen.TeamMembership, error) {
	return f.directory.ListTeamMembers(ctx, team)
}
func (f *installedReadStore) GetTeamOrgID(ctx context.Context, team string) (string, error) {
	return f.directory.GetTeamOrgID(ctx, team)
}

// A consuming module starts with a person-owned context addressed to itself,
// uses its declared installed binding, and forwards the returned context through
// the generated directory client. No directory fact crosses the authority API.
func TestDirectoryInstalledModuleCaller(t *testing.T) {
	f, authority := installedReadFixture(t, func(prefix string, manifest *solutionhost.Authority, grant map[string]any) {
		if prefix != "reader" {
			return
		}
		manifest.Bindings = append(manifest.Bindings, solutionhost.AuthorityBinding{
			ID: "directory", Audience: business.DirectoryAudience,
			Operations: map[string][]string{"invoke": {"tickets:read"}},
		})
		grant["operation_audiences"].(map[string]any)["directory"] = map[string]any{
			"audience": business.DirectoryAudience, "delegation_mode": "module_actor",
			"invoke_scopes": []any{map[string]any{"resource_kind": "tickets", "actions": []string{"read"}}},
		}
	})
	f.directory = &directoryContextStore{member: true}
	authority.journal = &moduleActorJournal{}
	minter := &fixedAccessMinter{identity: &auth.Identity{UserID: uuid.MustParse(readOwner), OrgID: uuid.MustParse(readOrg)}}
	mux := http.NewServeMux()
	options := connect.WithInterceptors(connectAuthInterceptor(func() auth.JWTMinter { return minter }))
	mux.Handle(accountsv1connect.NewDirectoryServiceHandler(&directoryConnectHandler{inner: &DirectoryServer{}}, options))
	mux.Handle(accountsv1connect.NewWorkContextServiceHandler(&workContextConnectHandler{inner: authority}, options))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	contexts := accountsv1connect.NewWorkContextServiceClient(server.Client(), server.URL)
	start := connect.NewRequest(&gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader",
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "tickets", Actions: []string{"read"}}}})
	start.Header().Set("Authorization", "Bearer session")
	parent, err := contexts.StartTask(t.Context(), start)
	require.NoError(t, err)
	moduleToken, _, err := authority.StartModuleTask(t.Context(), business.ModuleWorkContextAuthority{
		PrincipalID: business.ModulePrincipalID("reader"), Tenant: readOrg, InstallationID: f.live[business.ModulePrincipalID("reader")].InstallationID,
	})
	require.NoError(t, err)
	// The existing exchange is served on the module gRPC endpoint; use the
	// generated Connect client's gRPC transport, as production consumers do.
	internal := grpc.NewServer(grpc.UnaryInterceptor(grpcAuthInterceptor(nil, rpcExposureModule)))
	gen.RegisterModuleCapabilitiesServiceServer(internal, &ModuleCapabilitiesServer{})
	t.Cleanup(internal.Stop)
	exchangeServer := httptest.NewUnstartedServer(multiplexInternalGRPC(internal, http.NotFoundHandler()))
	exchangeServer.EnableHTTP2 = true
	exchangeServer.StartTLS()
	t.Cleanup(exchangeServer.Close)
	exchanges := accountsv1connect.NewModuleCapabilitiesServiceClient(exchangeServer.Client(), exchangeServer.URL, connect.WithGRPC())
	request := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "directory", ParentWorkContextToken: parent.Msg.Token})
	request.Header().Set(workContextHeader, moduleToken)
	request.Header().Set("x-codefly-internal-token", "installed-read-test")
	child, err := exchanges.ExchangeDelegatedOperationAudience(t.Context(), request)
	require.NoError(t, err)
	decoded, err := workcontext.Decode(child.Msg.Token)
	require.NoError(t, err)
	require.Equal(t, business.DirectoryAudience, decoded.Context().Audience)
	require.Equal(t, business.ModulePrincipalID("reader"), decoded.Context().ActorChain[0].PrincipalId)
	directory := accountsv1connect.NewDirectoryServiceClient(server.Client(), server.URL)
	for _, err := range callDirectory(t, directory, child.Msg.Token, "", readOrg, sourceA) {
		require.NoError(t, err)
	}
	for _, err := range callDirectory(t, directory, child.Msg.Token, "", foreignOrgID, sourceB) {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	}
	for _, err := range callDirectory(t, directory, parent.Msg.Token, "", readOrg, sourceA) {
		require.ErrorContains(t, err, "minted for audience")
	}
	// Withdrawing the installed binding refuses a previously issued child.
	registry := service.ModulePrincipals()
	grant := registry[business.ModulePrincipalID("reader")]
	delete(grant.OperationAudiences, "directory")
	registry[business.ModulePrincipalID("reader")] = grant
	service.InstallDerivedModulePrincipals(registry, f.envelope.Revision, nil)
	for _, err := range callDirectory(t, directory, child.Msg.Token, "", readOrg, sourceA) {
		require.Error(t, err)
		require.ErrorContains(t, err, "sealed to binding")
	}
	require.Equal(t, 3, f.directory.reads)
}
