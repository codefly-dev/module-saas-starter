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

// conformanceDev is another of Acme's administrators.
const conformanceDev = "00000000-0000-7000-8000-0000000000b4"

// testSolutionSourcesAreGovernedByTheirScope holds the host's datasource
// surface to a solution's own declaration: a source tied to a solution's
// installation is changed only by someone the authority core issues the
// governing scope there, an organization administrator included, and read
// only by a holder of a role on it; an organization's own source is decided
// by the organization rule alone, as before. Every call goes through the
// Connect listener, as a page's would, with each person's own session.
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
	// Alex administers the organization and edits; Dev administers it and
	// views; Maya, a member, views; Alice administers it and holds no role.
	for person, role := range map[string]string{conformanceAlex: "editor", conformanceDev: "viewer", conformanceMaya: "viewer"} {
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
	alex, dev, alice, maya, nora := signIn("fixture-alex"), signIn("fixture-dev"), signIn("dev-alice"), signIn("fixture-maya"), signIn("fixture-no-roles")
	connect := func(as context.Context, label string, governance *gen.SourceGovernance) (*gen.Datasource, error) {
		out, err := sources.AddSource(as, &gen.AddSourceRequest{OrgId: fixtureOrganization, Provider: gen.DatasourceProvider_DATASOURCE_PROVIDER_API,
			Config:   &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{BaseUrl: "https://api.example.test", CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER}},
			Boundary: &gen.AddSourceRequest_CollectionLabel{CollectionLabel: label}, Credential: "example-token", Governance: governance})
		return out.GetDatasource(), err
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
	remove := func(as context.Context, id string) error {
		_, err := sources.DeleteSource(as, &gen.DeleteSourceRequest{OrgId: fixtureOrganization, Id: id})
		return err
	}
	byEditors := &gen.SourceGovernance{InstallationId: inst.ID, ResourceKind: page, Action: "write"}

	t.Run("an organization's own source is decided by the organization rule alone", func(t *testing.T) {
		own, err := connect(dev, "Governed sources: the organization's", nil)
		require.NoError(t, err, "an administrator holding no editor role connects the organization's own source")
		require.Nil(t, own.GetGovernance())
		require.True(t, listed(nora, own.GetId()), "every member lists it")
		require.NoError(t, remove(alice, own.GetId()), "and any administrator removes it")
	})

	t.Run("a governed source is connected only by someone issued its governing scope", func(t *testing.T) {
		_, err := connect(dev, "Governed sources: a viewer's", byEditors)
		refused(t, err, codes.PermissionDenied, "may not connect a source governed by "+page+":write", inst.ID)
		_, err = connect(alice, "Governed sources: an outsider's", byEditors)
		refused(t, err, codes.PermissionDenied, "may not connect a source governed by "+page+":write")
		_, err = connect(maya, "Governed sources: a member's", byEditors)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "the organization rule still holds first: %v", err)
		_, err = connect(alex, "Governed sources: elsewhere", &gen.SourceGovernance{InstallationId: "no-such-installation", ResourceKind: page, Action: "write"})
		refused(t, err, codes.FailedPrecondition, "no-such-installation is not an active installation")
	})

	governed, err := connect(alex, "Governed sources: the editors'", byEditors)
	require.NoError(t, err, "an administrator who edits connects a source governed by the editors' scope")
	require.Equal(t, inst.ID, governed.GetGovernance().GetInstallationId())
	require.Equal(t, page, governed.GetGovernance().GetResourceKind())

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

	t.Run("a governed source is changed only by someone issued its governing scope", func(t *testing.T) {
		for name, as := range map[string]context.Context{"an administrator who views": dev, "an administrator holding no role": alice} {
			refused(t, sync(as, governed.GetId()), codes.PermissionDenied, "belongs to installation "+inst.ID, "requires "+page+":write")
			refused(t, remove(as, governed.GetId()), codes.PermissionDenied, "belongs to installation "+inst.ID)
			_, err := sources.UpdateSource(as, &gen.UpdateSourceRequest{OrgId: fixtureOrganization, Id: governed.GetId(), Expected: &gen.SourceFetchScope{}})
			refused(t, err, codes.PermissionDenied, "belongs to installation "+inst.ID)
			require.True(t, listed(alex, governed.GetId()), "%s changed nothing", name)
		}
		require.Equal(t, codes.PermissionDenied, status.Code(remove(maya, governed.GetId())), "a viewer who administers nothing changes nothing")
		require.True(t, listed(alex, governed.GetId()))
	})

	t.Run("the governing scope may be one the solution's roles reach through its binding", func(t *testing.T) {
		throughDocuments := &gen.SourceGovernance{InstallationId: inst.ID, ResourceKind: "example-records", Action: "write", Binding: "documents"}
		_, err := connect(dev, "Governed sources: a viewer's records", throughDocuments)
		refused(t, err, codes.PermissionDenied, "example-records:write through binding documents")
		records, err := connect(alex, "Governed sources: the editors' records", throughDocuments)
		require.NoError(t, err)
		refused(t, remove(dev, records.GetId()), codes.PermissionDenied, "belongs to installation "+inst.ID)
		require.NoError(t, remove(alex, records.GetId()), "an editor removes it")
	})

	t.Run("an editor changes the governed source", func(t *testing.T) {
		require.NoError(t, remove(alex, governed.GetId()))
		require.False(t, listed(alex, governed.GetId()))
	})

	t.Run("an uninstalled installation's sources are the organization's again", func(t *testing.T) {
		lapsing, err := connect(alex, "Governed sources: outliving the installation", byEditors)
		require.NoError(t, err)
		require.NoError(t, h.core.UninstallAsHost(h.ctx, fixtureOrganization, inst.ID))
		require.True(t, listed(nora, lapsing.GetId()), "every member lists it")
		require.NoError(t, remove(alice, lapsing.GetId()), "and any administrator removes what no role governs any more")
	})
}
