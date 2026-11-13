package adapters

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	"accounts/pkg/datasource/connector/connectortest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// testAMemberConnectsAPersonalSourceOfTheirOwn holds the host to what a
// personal source is: an api source connected with the person's own OAuth 2.0
// authorization-code sign-in, readable and callable by that person alone. It
// administers nothing, so a member connects one and declares its operations
// without the organization administrator role, where whoever answers for the
// session holds datasource:invoke — the person, or the team a cast acts as and
// nothing of the person's own. The source is always the person's, never the
// team's. Every other connect stays as it was: an organization's own source is
// its administrators', and a role view, which this host cannot narrow by
// subject, is refused. Every call goes through the gRPC listener with each
// person's own session.
func testAMemberConnectsAPersonalSourceOfTheirOwn(t *testing.T, h reviewedClosureHarness) {
	const target = "conformance-personal-sources"
	page := target + ".page"
	deliverSolution(t, h.ctx, h.conn, h.signer, conformanceSolution{alias: target, generation: 1,
		ceilings: []*authorityv1.WorkScopeV1{wireScope(page, "read")},
		roles:    map[string][]*authorityv1.WorkScopeV1{"installer": {wireScope(page, "read")}, "viewer": {wireScope(page, "read")}},
	})
	inst, err := installConformanceSolution(h.ctx, h.svc, h.core, target, "viewer", conformanceAlex)
	require.NoError(t, err)

	h.svc.SetDatasourceConnector(staticCipher{}, nil, "")
	require.NoError(t, h.svc.ConfigureDatasourceOAuth(
		`{"api":{"authorize_url":"https://provider.example.test/authorize","token_url":"https://provider.example.test/token"}}`,
		`{"api":"conformance-client"}`, `{"api":"conformance-secret"}`))
	h.svc.SetDatasourceKeys([]byte("conformance-ticket-key"), []byte("conformance-link-key"))
	t.Cleanup(func() {
		require.NoError(t, h.svc.ConfigureDatasourceOAuth("", "", ""))
		h.svc.SetDatasourceKeys(nil, nil)
	})

	sources := gen.NewDatasourceServiceClient(h.conn)
	signIn := func(token string) context.Context {
		t.Helper()
		session, err := sessionv1.NewSessionServiceClient(h.conn).SignIn(h.ctx, &sessionv1.SignInRequest{FixtureToken: token})
		require.NoError(t, err)
		return metadata.AppendToOutgoingContext(h.ctx, "authorization", "Bearer "+session.GetBearer())
	}
	bearing := func(bearer string) context.Context {
		return metadata.AppendToOutgoingContext(h.ctx, "authorization", "Bearer "+bearer)
	}
	remi, alex, dev, nora := signIn("fixture-remi"), signIn("fixture-alex"), signIn("fixture-dev"), signIn("fixture-no-roles")
	refused := func(t *testing.T, err error, code codes.Code, reason ...string) {
		t.Helper()
		require.Equal(t, code, status.Code(err), "%v", err)
		for _, r := range reason {
			require.Contains(t, status.Convert(err).Message(), r)
		}
	}

	// Two collections: one an administrator lets people call sources in, one
	// nobody was granted anything on.
	node := func(label string) *gen.ScopeNode {
		t.Helper()
		id := strings.ReplaceAll(uuid.NewString(), "-", "_")
		out, err := h.svc.RegisterScopeNode(h.ctx, conformanceAlex, &gen.RegisterScopeNodeRequest{OrgId: fixtureOrganization, ScopePath: id, Kind: "collection", Label: label})
		require.NoError(t, err)
		return out.GetNode()
	}
	granted, elsewhere := node("Personal sources: callable"), node("Personal sources: not granted")
	callable, err := h.svc.CreateRole(h.ctx, conformanceAlex, &gen.CreateRoleRequest{OrgId: fixtureOrganization, Name: "Personal sources: callable",
		Permissions: []*gen.Permission{{Resource: "datasource", Action: "invoke"}, {Resource: "datasource", Action: "read"}}})
	require.NoError(t, err)
	grant := func(subject string, kind gen.SubjectKind, at *gen.ScopeNode) {
		t.Helper()
		_, err := h.svc.GrantScope(h.ctx, conformanceAlex, &gen.GrantScopeRequest{OrgId: fixtureOrganization, SubjectId: subject, SubjectKind: kind, ScopePath: at.GetScopePath(), RoleId: callable.GetRole().GetId()})
		require.NoError(t, err)
	}

	personal := func(as context.Context, boundary *gen.AddSourceRequest) (*gen.Datasource, error) {
		out, err := sources.AddSource(as, &gen.AddSourceRequest{OrgId: fixtureOrganization, Provider: gen.DatasourceProvider_DATASOURCE_PROVIDER_API,
			Config: &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{BaseUrl: "https://api.example.test",
				CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_OAUTH2, Oauth2: &gen.ApiOAuth2Config{Grant: gen.ApiOAuth2Config_GRANT_AUTHORIZATION_CODE}}},
			Boundary: boundary.GetBoundary()})
		return out.GetDatasource(), err
	}
	shared := func(as context.Context, at *gen.ScopeNode) error {
		_, err := sources.AddSource(as, &gen.AddSourceRequest{OrgId: fixtureOrganization, Provider: gen.DatasourceProvider_DATASOURCE_PROVIDER_API,
			Config:   &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{BaseUrl: "https://api.example.test", CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER}},
			Boundary: &gen.AddSourceRequest_BoundaryNodeId{BoundaryNodeId: at.GetId()}, Credential: "example-token"})
		return err
	}
	under := func(at *gen.ScopeNode) *gen.AddSourceRequest {
		return &gen.AddSourceRequest{Boundary: &gen.AddSourceRequest_BoundaryNodeId{BoundaryNodeId: at.GetId()}}
	}
	labelled := func(label string) *gen.AddSourceRequest {
		return &gen.AddSourceRequest{Boundary: &gen.AddSourceRequest_CollectionLabel{CollectionLabel: label}}
	}
	declare := func(as context.Context, id string) error {
		_, err := sources.DeclareSourceOperations(as, &gen.DeclareSourceOperationsRequest{OrgId: fixtureOrganization, SourceId: id})
		return err
	}
	link := func(as context.Context, id string) (*gen.BeginDatasourceAccountLinkResponse, error) {
		return sources.BeginDatasourceAccountLink(as, &gen.BeginDatasourceAccountLinkRequest{OrgId: fixtureOrganization, Connector: "api", SourceId: id, RedirectUri: "https://page.example.test/back"})
	}
	listed := func(as context.Context, id string) bool {
		t.Helper()
		out, err := sources.ListSources(as, &gen.ListSourcesRequest{OrgId: fixtureOrganization})
		require.NoError(t, err)
		return slices.ContainsFunc(out.GetDatasources(), func(d *gen.Datasource) bool { return d.GetId() == id })
	}
	remove := func(as context.Context, id string) {
		t.Helper()
		_, err := sources.DeleteSource(as, &gen.DeleteSourceRequest{OrgId: fixtureOrganization, Id: id})
		require.NoError(t, err)
	}

	t.Run("a member needs datasource:invoke where their own source is placed", func(t *testing.T) {
		_, err := personal(remi, under(granted))
		refused(t, err, codes.PermissionDenied, "you do not hold datasource:invoke on scope node "+granted.GetId())
		require.True(t, strings.HasPrefix(status.Convert(err).Message(), "you do not hold"), "the refusal is the caller's, not a failure to persist: %v", err)
		_, err = personal(nora, under(granted))
		refused(t, err, codes.PermissionDenied, "datasource:invoke")
		// A node that does not exist is refused exactly as one not granted,
		// so a member learns nothing of which scope nodes exist.
		absent := uuid.NewString()
		_, err = personal(nora, under(&gen.ScopeNode{Id: absent}))
		refused(t, err, codes.PermissionDenied, "you do not hold datasource:invoke on scope node "+absent)
	})

	grant(conformanceRemi, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, granted)
	var own *gen.Datasource
	t.Run("a member connects a personal source of their own, and it is theirs alone", func(t *testing.T) {
		own, err = personal(remi, under(granted))
		require.NoError(t, err, "a member holding datasource:invoke connects their own source without administering the organization")
		require.Equal(t, granted.GetId(), own.GetBoundaryNodeId())
		require.True(t, listed(remi, own.GetId()), "its owner lists it")
		require.False(t, listed(nora, own.GetId()), "another member does not")
		require.False(t, listed(dev, own.GetId()), "nor does an administrator")
		delegations, err := sources.ListSourceDelegations(dev, &gen.ListSourceDelegationsRequest{OrgId: fixtureOrganization})
		require.NoError(t, err)
		require.False(t, slices.ContainsFunc(delegations.GetDelegations(), func(d *gen.SourceDelegation) bool { return d.GetSourceId() == own.GetId() }),
			"a member's own source delegates nothing to a module")
	})

	t.Run("a member's own source goes only under an existing node they may call sources in", func(t *testing.T) {
		_, err := personal(remi, under(elsewhere))
		refused(t, err, codes.PermissionDenied, "you do not hold datasource:invoke on scope node "+elsewhere.GetId())
		_, err = personal(remi, labelled("Personal sources: a member's new collection"))
		refused(t, err, codes.PermissionDenied, "existing scope node", "organization administrator")
	})

	t.Run("anything but a personal source is still an administrator's to connect", func(t *testing.T) {
		refused(t, shared(remi, granted), codes.PermissionDenied, "requires organization administrator or owner role")
	})

	t.Run("its owner declares its operations and signs in for it; nobody else does", func(t *testing.T) {
		require.NoError(t, declare(remi, own.GetId()), "the owner declares on their own source")
		refused(t, declare(nora, own.GetId()), codes.PermissionDenied)
		refused(t, declare(dev, own.GetId()), codes.PermissionDenied, "personal to another member")
		refused(t, declare(remi, uuid.NewString()), codes.PermissionDenied)
		begun, err := link(remi, own.GetId())
		require.NoError(t, err, "the owner signs in for their own source")
		require.Contains(t, begun.GetAuthorizeUrl(), "https://provider.example.test/authorize")
		_, err = link(nora, own.GetId())
		refused(t, err, codes.PermissionDenied)
	})

	t.Run("a cast connects its person's own source, decided by its team alone", func(t *testing.T) {
		team, err := h.svc.CreateTeam(h.ctx, conformanceAlex, &gen.CreateTeamRequest{OrgId: fixtureOrganization, Name: "Personal sources: casting"})
		require.NoError(t, err)
		installations := installationv1.NewInstallationServiceClient(h.conn)
		_, err = installations.GrantRole(alex, &installationv1.GrantRoleRequest{OrganizationId: fixtureOrganization, InstallationId: inst.ID, Role: "viewer", TeamId: team.GetTeam().GetId()})
		require.NoError(t, err)
		cast, err := sessionv1.NewSessionServiceClient(h.conn).OpenTeamCast(alex, &sessionv1.OpenTeamCastRequest{TeamId: team.GetTeam().GetId()})
		require.NoError(t, err)
		casting := bearing(cast.GetBearer())

		// The administrator's own grants are not the cast's: the team holds
		// nothing where the source would go, so the cast is refused there.
		grant(conformanceAlex, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, granted)
		_, err = personal(casting, under(granted))
		refused(t, err, codes.PermissionDenied, "team "+team.GetTeam().GetId()+", which this session acts as, does not hold datasource:invoke")

		grant(team.GetTeam().GetId(), gen.SubjectKind_SUBJECT_KIND_TEAM, granted)
		mine, err := personal(casting, under(granted))
		require.NoError(t, err, "the cast connects once its team may call sources there")
		require.True(t, listed(alex, mine.GetId()), "the source is its person's, listed in the session the cast was opened from")
		require.False(t, listed(nora, mine.GetId()))
		require.NoError(t, declare(casting, mine.GetId()), "the cast declares on its person's own source")
		_, err = link(casting, mine.GetId())
		require.NoError(t, err, "and signs in for it")

		// Everything else stays refused to the cast, by name.
		refused(t, shared(casting, granted), codes.PermissionDenied, "acts as team")
		_, err = personal(casting, labelled("Personal sources: a cast's new collection"))
		refused(t, err, codes.PermissionDenied, "existing scope node")
		refused(t, declare(casting, own.GetId()), codes.PermissionDenied, "acts as team")
		_, err = link(casting, own.GetId())
		refused(t, err, codes.PermissionDenied, "acts as team")
		_, err = sources.BeginDatasourceAccountLink(casting, &gen.BeginDatasourceAccountLinkRequest{OrgId: fixtureOrganization, Connector: "github", SourceId: mine.GetId()})
		refused(t, err, codes.PermissionDenied, "acts as team")

		// A source of the person's own placed where the team holds nothing —
		// connected in the session the cast was opened from — is not the
		// cast's to sign in for: neither beginning a sign-in nor completing
		// one begun in that other session. Ownership is the person's; the
		// cast holds only what the team holds.
		outside, err := personal(alex, under(elsewhere))
		require.NoError(t, err, "an administrator connects their own source anywhere in their own session")
		teamLacks := "team " + team.GetTeam().GetId() + ", which this session acts as, does not hold datasource:invoke on scope node " + elsewhere.GetId()
		refused(t, declare(casting, outside.GetId()), codes.PermissionDenied, teamLacks)
		_, err = link(casting, outside.GetId())
		refused(t, err, codes.PermissionDenied, teamLacks)
		begunElsewhere, err := link(alex, outside.GetId())
		require.NoError(t, err)
		_, err = sources.CompleteDatasourceAccountLink(casting, &gen.CompleteDatasourceAccountLinkRequest{OrgId: fixtureOrganization, State: begunElsewhere.GetState(), Code: "conformance-code"})
		refused(t, err, codes.PermissionDenied, teamLacks)
		remove(alex, outside.GetId())
		remove(alex, mine.GetId())
	})

	t.Run("a cast reads and calls its person's own source by its team's grants alone", func(t *testing.T) {
		// The person holds datasource:invoke and datasource:read where the
		// source is placed; the team the cast acts as holds nothing there yet.
		calledAt := node("Personal sources: called from a cast")
		grant(conformanceAlex, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, calledAt)
		src, err := personal(alex, under(calledAt))
		require.NoError(t, err)
		declared, err := declarationToProto(connectortest.OperationDeclaration())
		require.NoError(t, err)
		_, err = sources.DeclareSourceOperations(alex, &gen.DeclareSourceOperationsRequest{OrgId: fixtureOrganization, SourceId: src.GetId(), Operations: []*gen.SourceOperation{declared}})
		require.NoError(t, err)
		listOperations := func(as context.Context) (*gen.ListSourceOperationsResponse, error) {
			return sources.ListSourceOperations(as, &gen.ListSourceOperationsRequest{OrgId: fixtureOrganization, SourceId: src.GetId()})
		}
		invoke := func(as context.Context) error {
			_, err := sources.InvokeSourceOperation(as, &gen.InvokeSourceOperationRequest{OrgId: fixtureOrganization, SourceId: src.GetId(),
				Operation: declared.GetName(), InputJson: `{"id":"one"}`, EffectId: uuid.NewString()})
			return err
		}
		read := func(as context.Context) error {
			_, err := sources.GetSource(as, &gen.GetSourceRequest{OrgId: fixtureOrganization, Id: src.GetId()})
			return err
		}
		// Calls the person made in their own session, whose receipts a lookup
		// recovers: the effect's attempt names the person, never the session.
		attempted := func(operation string) string {
			effect := uuid.NewString()
			require.NoError(t, h.store.WithOrgTx(h.ctx, fixtureOrganization, func(ctx context.Context) error {
				_, err := h.store.CreateSourceOperationAttempt(ctx, business.SourceOperationAttempt{OrgID: fixtureOrganization, EffectID: effect,
					ActorID: conformanceAlex, SourceID: src.GetId(), Operation: operation, DeclarationDigest: "conformance", RequestDigest: []byte("conformance")})
				return err
			}))
			return effect
		}
		invoked, pruned, reconciled := attempted(declared.GetName()), attempted("_receipt_retention"), attempted("_reconcile")
		lookups := map[string]func(context.Context) error{
			"an invoke's lookup": func(as context.Context) error {
				_, err := sources.LookupInvokeSourceOperation(as, &gen.LookupInvokeSourceOperationRequest{EffectId: invoked})
				return err
			},
			"a receipt prune's lookup": func(as context.Context) error {
				_, err := sources.LookupPruneSourceOperationReceipts(as, &gen.LookupPruneSourceOperationReceiptsRequest{EffectId: pruned})
				return err
			},
		}
		lookUpReconcile := func(as context.Context) error {
			_, err := sources.LookupReconcileSource(as, &gen.LookupReconcileSourceRequest{EffectId: reconciled})
			return err
		}
		// Each lookup is admitted and reaches the receipt store, which this
		// harness does not configure: past every authority check.
		pastAuthority := func(t *testing.T, err error) {
			t.Helper()
			refused(t, err, codes.Unavailable, "source receipts unavailable")
		}
		for _, lookUp := range lookups {
			pastAuthority(t, lookUp(alex))
		}
		pastAuthority(t, lookUpReconcile(alex))
		inOwnSession, err := listOperations(alex)
		require.NoError(t, err, "the person reads their own source's operations in their own session")
		require.Len(t, inOwnSession.GetOperations(), 1)
		require.NoError(t, read(alex))
		require.True(t, listed(alex, src.GetId()))

		team, err := h.svc.CreateTeam(h.ctx, conformanceAlex, &gen.CreateTeamRequest{OrgId: fixtureOrganization, Name: "Personal sources: calling"})
		require.NoError(t, err)
		_, err = installationv1.NewInstallationServiceClient(h.conn).GrantRole(alex, &installationv1.GrantRoleRequest{OrganizationId: fixtureOrganization, InstallationId: inst.ID, Role: "viewer", TeamId: team.GetTeam().GetId()})
		require.NoError(t, err)
		cast, err := sessionv1.NewSessionServiceClient(h.conn).OpenTeamCast(alex, &sessionv1.OpenTeamCastRequest{TeamId: team.GetTeam().GetId()})
		require.NoError(t, err)
		casting := bearing(cast.GetBearer())

		// The person's grant is theirs, not the cast's: the team holds
		// nothing where the source is placed, so the cast neither reads the
		// source, nor its operations, nor calls it.
		_, err = listOperations(casting)
		refused(t, err, codes.PermissionDenied, "team "+team.GetTeam().GetId()+", which this session acts as, does not hold datasource:read on scope node "+calledAt.GetId())
		refused(t, invoke(casting), codes.PermissionDenied, "team "+team.GetTeam().GetId()+", which this session acts as, does not hold datasource:invoke on scope node "+calledAt.GetId())
		refused(t, read(casting), codes.PermissionDenied, "team "+team.GetTeam().GetId()+", which this session acts as, does not hold datasource:read on scope node "+calledAt.GetId())
		require.False(t, listed(casting, src.GetId()), "a cast lists no source of its person's where its team may not read it")
		for _, lookUp := range lookups {
			refused(t, lookUp(casting), codes.PermissionDenied, "team "+team.GetTeam().GetId()+", which this session acts as, does not hold datasource:read on scope node "+calledAt.GetId())
		}
		// A reconcile is decided by ownership and administration, which a
		// cast holds neither of: its receipt is refused to the cast by name.
		refused(t, lookUpReconcile(casting), codes.PermissionDenied, "acts as team")

		// Once the team holds both there, the cast is answered.
		grant(team.GetTeam().GetId(), gen.SubjectKind_SUBJECT_KIND_TEAM, calledAt)
		answered, err := listOperations(casting)
		require.NoError(t, err, "the cast reads the operations once its team may read sources there")
		require.Len(t, answered.GetOperations(), 1)
		require.NoError(t, read(casting))
		require.True(t, listed(casting, src.GetId()))
		// The call is admitted and reaches the receipt guard: past every
		// authority check, refused for a reason that is not the cast's.
		pastAuthority(t, invoke(casting))
		for _, lookUp := range lookups {
			pastAuthority(t, lookUp(casting))
		}
		refused(t, lookUpReconcile(casting), codes.PermissionDenied, "acts as team")

		// The team holding datasource:invoke where an organization's own
		// source is placed does not make it the cast's to call: a cast calls
		// only a source of its person's own.
		organizations, err := sources.AddSource(alex, &gen.AddSourceRequest{OrgId: fixtureOrganization, Provider: gen.DatasourceProvider_DATASOURCE_PROVIDER_API,
			Config:   &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{BaseUrl: "https://api.example.test", CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER}},
			Boundary: &gen.AddSourceRequest_BoundaryNodeId{BoundaryNodeId: calledAt.GetId()}, Credential: "example-token"})
		require.NoError(t, err)
		_, err = sources.InvokeSourceOperation(casting, &gen.InvokeSourceOperationRequest{OrgId: fixtureOrganization, SourceId: organizations.GetDatasource().GetId(),
			Operation: declared.GetName(), InputJson: `{"id":"one"}`, EffectId: uuid.NewString()})
		refused(t, err, codes.PermissionDenied, "a cast calls only a source of its person's own")
		remove(alex, organizations.GetDatasource().GetId())

		// The provider accounts linked to the person are theirs, not the team's.
		_, err = sources.ListMyDatasourceAccountLinks(casting, &gen.ListMyDatasourceAccountLinksRequest{OrgId: fixtureOrganization})
		refused(t, err, codes.PermissionDenied, "acts as team", "provider accounts linked to a person are their own")
		_, err = sources.ListMyDatasourceAccountLinks(alex, &gen.ListMyDatasourceAccountLinksRequest{OrgId: fixtureOrganization})
		require.NoError(t, err, "the person reads their own links in their own session")

		// The person's own session and another member are unchanged.
		_, err = listOperations(alex)
		require.NoError(t, err)
		_, err = listOperations(nora)
		refused(t, err, codes.PermissionDenied)
		remove(alex, src.GetId())
	})

	t.Run("a role view connects no source", func(t *testing.T) {
		held, err := installationv1.NewInstallationServiceClient(h.conn).ListHeld(alex, &installationv1.ListHeldRequest{OrganizationId: fixtureOrganization})
		require.NoError(t, err)
		require.NotEmpty(t, held.GetInstallations())
		one := held.GetInstallations()[0]
		view, err := sessionv1.NewSessionServiceClient(h.conn).OpenRoleView(alex, &sessionv1.OpenRoleViewRequest{InstallationId: one.GetInstallation().GetId(), Roles: one.GetRoles()[:1]})
		require.NoError(t, err)
		_, err = personal(bearing(view.GetBearer()), under(granted))
		refused(t, err, codes.PermissionDenied, "this session views as")
	})

	remove(dev, own.GetId())
}
