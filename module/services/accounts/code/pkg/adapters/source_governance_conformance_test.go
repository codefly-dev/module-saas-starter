package adapters

import (
	"context"
	"slices"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	authorityv1 "accounts/pkg/gen/host/authority/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The harness's people this case decides for, beside Alex, Alice and Maya:
// Dev administers Acme; Remi is a member and administers nothing.
const (
	conformanceDev  = "00000000-0000-7000-8000-0000000000b4"
	conformanceRemi = "00000000-0000-7000-8000-0000000000b2"
)

// testSolutionSourcesAreGovernedByTheirScope holds the host's datasource
// surface to a solution's own declaration: a source tied to a solution's
// installation is connected, changed, synced, linked and removed by whoever
// the authority core issues its governing scope there, whether or not they
// administer the organization, and by nobody else, an administrator included;
// it is read by the holders of a role on the installation, and writes only
// into the installation's own collections. An organization's own source stays
// its administrators' to change. Every call goes through the Connect listener,
// as a page's would, with each person's own session.
func testSolutionSourcesAreGovernedByTheirScope(t *testing.T, h reviewedClosureHarness) {
	const target = "conformance-governed-sources"
	page := target + ".page"
	// Editors write the solution's pages and, through its documents binding,
	// example records; viewers only read its pages.
	deliverSolution(t, h.ctx, h.conn, h.signer, conformanceSolution{alias: target, generation: 1,
		ceilings: []*authorityv1.WorkScopeV1{wireScope(page, "read", "write")},
		roles: map[string][]*authorityv1.WorkScopeV1{
			"installer": {wireScope(page, "read")},
			"editor":    {wireScope(page, "read", "write"), wireScope("example-records", "write")},
			"viewer":    {wireScope(page, "read")},
		},
		bindings: []solutionhost.AuthorityBinding{{ID: "documents", Audience: "documents", Operations: map[string][]string{"invoke": {"example-records:write"}}}},
	})
	inst, err := installConformanceSolution(h.ctx, h.svc, h.core, target, "viewer", conformanceAlex)
	require.NoError(t, err)
	// Remi edits and administers nothing; Alex edits and administers; Dev
	// administers and views; Maya views; Alice administers and holds no role;
	// Nora is a member holding nothing.
	for person, role := range map[string]string{conformanceRemi: "editor", conformanceAlex: "editor", conformanceDev: "viewer", conformanceMaya: "viewer"} {
		_, err := h.core.GrantRoleAsHost(h.ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: inst.ID, Role: role, PersonID: person})
		require.NoError(t, err)
	}

	// The host's own datasource configuration, as a test wires it: a cipher
	// that seals nothing and no sync producer, so a connect stores the row and
	// nothing reaches out.
	h.svc.SetDatasourceConnector(staticCipher{}, nil, "")
	sources := gen.NewDatasourceServiceClient(h.conn)
	signIn := func(token string) context.Context {
		t.Helper()
		session, err := sessionv1.NewSessionServiceClient(h.conn).SignIn(h.ctx, &sessionv1.SignInRequest{FixtureToken: token})
		require.NoError(t, err)
		return metadata.AppendToOutgoingContext(h.ctx, "authorization", "Bearer "+session.GetBearer())
	}
	remi, alex, dev, alice, maya, nora := signIn("fixture-remi"), signIn("fixture-alex"), signIn("fixture-dev"), signIn("dev-alice"), signIn("fixture-maya"), signIn("fixture-no-roles")
	connectTo := func(as context.Context, boundary *gen.AddSourceRequest, governance *gen.SourceGovernance) (*gen.Datasource, error) {
		request := &gen.AddSourceRequest{OrgId: fixtureOrganization, Provider: gen.DatasourceProvider_DATASOURCE_PROVIDER_API,
			Config:   &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{BaseUrl: "https://api.example.test", CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER}},
			Boundary: boundary.GetBoundary(), Credential: "example-token", Governance: governance}
		out, err := sources.AddSource(as, request)
		return out.GetDatasource(), err
	}
	labelled := func(label string) *gen.AddSourceRequest {
		return &gen.AddSourceRequest{Boundary: &gen.AddSourceRequest_CollectionLabel{CollectionLabel: label}}
	}
	node := func(id string) *gen.AddSourceRequest {
		return &gen.AddSourceRequest{Boundary: &gen.AddSourceRequest_BoundaryNodeId{BoundaryNodeId: id}}
	}
	connect := func(as context.Context, label string, governance *gen.SourceGovernance) (*gen.Datasource, error) {
		return connectTo(as, labelled(label), governance)
	}
	refused := func(t *testing.T, err error, code codes.Code, reason ...string) {
		t.Helper()
		require.Equal(t, code, status.Code(err), "%v", err)
		for _, r := range reason {
			require.Contains(t, status.Convert(err).Message(), r)
		}
	}
	listed := func(as context.Context, id string) bool {
		t.Helper()
		out, err := sources.ListSources(as, &gen.ListSourcesRequest{OrgId: fixtureOrganization})
		require.NoError(t, err)
		return slices.ContainsFunc(out.GetDatasources(), func(d *gen.Datasource) bool { return d.GetId() == id })
	}
	sync := func(as context.Context, id string) error {
		_, err := sources.SyncSource(as, &gen.SyncSourceRequest{OrgId: fixtureOrganization, Id: id})
		return err
	}
	update := func(as context.Context, id string) error {
		_, err := sources.UpdateSource(as, &gen.UpdateSourceRequest{OrgId: fixtureOrganization, Id: id, Expected: &gen.SourceFetchScope{}})
		return err
	}
	remove := func(as context.Context, id string) error {
		_, err := sources.DeleteSource(as, &gen.DeleteSourceRequest{OrgId: fixtureOrganization, Id: id})
		return err
	}
	link := func(as context.Context, id string) error {
		_, err := sources.BeginDatasourceAccountLink(as, &gen.BeginDatasourceAccountLinkRequest{OrgId: fixtureOrganization, Connector: "api", SourceId: id})
		return err
	}
	byEditors := &gen.SourceGovernance{InstallationId: inst.ID, ResourceKind: page, Action: "write"}
	governedRefusal := "belongs to installation " + inst.ID

	// The organization's own source, and its collection, the governed sources
	// below must never write into.
	own, err := connect(dev, "Governed sources: the organization's", nil)
	require.NoError(t, err, "an administrator holding no role connects the organization's own source")
	require.Nil(t, own.GetGovernance())

	t.Run("an organization's own source stays its administrators' to change", func(t *testing.T) {
		require.True(t, listed(nora, own.GetId()), "every member lists it")
		_, err := connect(remi, "Governed sources: a member's own", nil)
		refused(t, err, codes.PermissionDenied)
		for _, change := range []func(context.Context, string) error{sync, update, remove} {
			refused(t, change(remi, own.GetId()), codes.PermissionDenied)
		}
		require.True(t, listed(nora, own.GetId()), "a member who edits a solution changed nothing of the organization's")
	})

	t.Run("a governed source is connected by whoever is issued its governing scope", func(t *testing.T) {
		_, err := connect(maya, "Governed sources: a viewer's", byEditors)
		refused(t, err, codes.PermissionDenied, "may not connect a source governed by "+page+":write", inst.ID)
		_, err = connect(dev, "Governed sources: an administrator who views", byEditors)
		refused(t, err, codes.PermissionDenied, "may not connect a source governed by "+page+":write")
		_, err = connect(alice, "Governed sources: an administrator holding nothing", byEditors)
		refused(t, err, codes.PermissionDenied, "may not connect a source governed by "+page+":write")
		_, err = connect(remi, "Governed sources: elsewhere", &gen.SourceGovernance{InstallationId: "no-such-installation", ResourceKind: page, Action: "write"})
		refused(t, err, codes.FailedPrecondition, "no-such-installation is not an active installation")
	})

	governed, err := connect(remi, "Governed sources: the editors'", byEditors)
	require.NoError(t, err, "an editor who administers nothing connects a source governed by the editors' scope")
	require.Equal(t, inst.ID, governed.GetGovernance().GetInstallationId())
	require.Equal(t, page, governed.GetGovernance().GetResourceKind())

	t.Run("a governed source writes only into its installation's own collections", func(t *testing.T) {
		_, err := connectTo(remi, node(own.GetBoundaryNodeId()), byEditors)
		refused(t, err, codes.PermissionDenied, "is not a collection of installation "+inst.ID)
		same, err := connect(remi, "Governed sources: the organization's", byEditors)
		require.NoError(t, err)
		require.NotEqual(t, own.GetBoundaryNodeId(), same.GetBoundaryNodeId(), "a label an organization collection bears mints the installation's own")
		again, err := connectTo(remi, node(governed.GetBoundaryNodeId()), byEditors)
		require.NoError(t, err, "an existing collection of the installation's may be named")
		require.Equal(t, governed.GetBoundaryNodeId(), again.GetBoundaryNodeId())
		reused, err := connect(remi, "Governed sources: the editors'", byEditors)
		require.NoError(t, err)
		require.Equal(t, governed.GetBoundaryNodeId(), reused.GetBoundaryNodeId(), "and a label reuses it")
		for _, id := range []string{same.GetId(), again.GetId(), reused.GetId()} {
			require.NoError(t, remove(remi, id))
		}
	})

	t.Run("a governed source is read by the holders of a role on its installation", func(t *testing.T) {
		require.True(t, listed(maya, governed.GetId()), "a viewer lists it")
		_, err := sources.GetSource(maya, &gen.GetSourceRequest{OrgId: fixtureOrganization, Id: governed.GetId()})
		require.NoError(t, err, "and reads it")
		require.True(t, listed(dev, governed.GetId()))
		require.False(t, listed(nora, governed.GetId()), "a member holding no role there does not list it")
		require.False(t, listed(alice, governed.GetId()), "nor does an administrator holding none")
		_, err = sources.GetSource(alice, &gen.GetSourceRequest{OrgId: fixtureOrganization, Id: governed.GetId()})
		refused(t, err, codes.PermissionDenied, "reading it requires a role there")
	})

	t.Run("a governed source is changed only by whoever is issued its governing scope", func(t *testing.T) {
		for name, as := range map[string]context.Context{"a viewer": maya, "an administrator who views": dev, "an administrator holding no role": alice, "a member holding nothing": nora} {
			refused(t, sync(as, governed.GetId()), codes.PermissionDenied, governedRefusal, "requires "+page+":write")
			refused(t, update(as, governed.GetId()), codes.PermissionDenied, governedRefusal)
			refused(t, remove(as, governed.GetId()), codes.PermissionDenied, governedRefusal)
			refused(t, link(as, governed.GetId()), codes.PermissionDenied, governedRefusal)
			require.True(t, listed(remi, governed.GetId()), "%s changed nothing", name)
		}
		// The editor passes the governance; what follows is the business's own
		// answer for an operations-only source and an unconfigured connector.
		for _, change := range []func(context.Context, string) error{sync, update, link} {
			if err := change(remi, governed.GetId()); err != nil {
				require.NotContains(t, status.Convert(err).Message(), governedRefusal)
				require.NotEqual(t, codes.PermissionDenied, status.Code(err), "%v", err)
			}
		}
	})

	t.Run("the governing scope may be one the solution's roles reach through its binding", func(t *testing.T) {
		throughDocuments := &gen.SourceGovernance{InstallationId: inst.ID, ResourceKind: "example-records", Action: "write", Binding: "documents"}
		_, err := connect(maya, "Governed sources: a viewer's records", throughDocuments)
		refused(t, err, codes.PermissionDenied, "example-records:write through binding documents")
		records, err := connect(remi, "Governed sources: the editors' records", throughDocuments)
		require.NoError(t, err)
		refused(t, remove(dev, records.GetId()), codes.PermissionDenied, governedRefusal)
		require.NoError(t, remove(alex, records.GetId()), "an editor who administers removes it too")
	})

	t.Run("an editor who administers nothing removes the governed source", func(t *testing.T) {
		require.NoError(t, remove(remi, governed.GetId()))
		require.False(t, listed(remi, governed.GetId()))
	})

	t.Run("an uninstalled installation's sources are the organization's again", func(t *testing.T) {
		lapsing, err := connect(remi, "Governed sources: outliving the installation", byEditors)
		require.NoError(t, err)
		require.NoError(t, h.core.UninstallAsHost(h.ctx, fixtureOrganization, inst.ID))
		require.True(t, listed(nora, lapsing.GetId()), "every member lists it")
		refused(t, remove(remi, lapsing.GetId()), codes.PermissionDenied)
		require.NoError(t, remove(alice, lapsing.GetId()), "and an administrator removes what no role governs any more")
		require.NoError(t, remove(alice, own.GetId()))
	})
}
