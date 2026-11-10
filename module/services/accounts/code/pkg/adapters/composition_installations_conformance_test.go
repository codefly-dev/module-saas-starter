package adapters

import (
	"context"
	"slices"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"
)

// testModulesInstalledByComposition holds the host to installing every
// composed module in every organization itself, holding no standing
// authority: at delivery, for a new organization, and on a restart, never
// twice; a module page so installed has no access until set up; a withdrawal
// revokes every installation.
func testModulesInstalledByComposition(t *testing.T, h reviewedClosureHarness) {
	orgs := []string{fixtureOrganization, globexOrganization}
	installed := func(org, module string) *gen.Installation {
		t.Helper()
		inst, err := h.svc.ActiveOrPendingModuleInstallation(h.ctx, org, module)
		require.NoError(t, err)
		return inst
	}

	t.Run("delivery installs a module in every existing organization", func(t *testing.T) {
		const module = "conformance-composed"
		deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{wireScope(module+".item", "read")}})
		for _, org := range orgs {
			inst := installed(org, module)
			require.NotNil(t, inst, "%s is installed at %s by its delivery", module, org)
			held, err := h.svc.ReadHostInstallation(h.ctx, org, inst.GetId())
			require.NoError(t, err)
			require.Empty(t, held.Role, "holding no standing authority")
		}
	})

	t.Run("a new organization gets every composed module", func(t *testing.T) {
		const org = "00000000-0000-7000-8000-0000000002c1"
		// Its owner belongs to no other organization, so nobody else's session
		// changes organization.
		const newcomer = "00000000-0000-7000-8000-0000000000e3"
		_, err := h.svc.CreateFixtureOrganization(h.ctx, newcomer, &gen.CreateOrganizationRequest{Name: "Composition New"}, org)
		require.NoError(t, err)
		for _, module := range []string{"conformance-composed", "documents", "org-directory"} {
			require.NotNil(t, installed(org, module), "%s is installed in the new organization", module)
		}
	})

	t.Run("a restart installs what is missing and nothing twice", func(t *testing.T) {
		const module = "conformance-composed-restart"
		h.faults.blockComposition(module)
		deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{wireScope(module+".item", "read")}})
		for _, org := range orgs {
			require.Nil(t, installed(org, module), "held back at %s", org)
		}
		h.faults.unblockComposition(module)
		// The pass a start and every delivery run; what other cases hold back
		// is reported, this module is not.
		pass := func() {
			if err := h.svc.KeepAuthorityCoreCurrent(h.ctx); err != nil {
				require.NotContains(t, err.Error(), module)
			}
		}
		pass()
		first := map[string]*gen.Installation{}
		for _, org := range orgs {
			first[org] = installed(org, module)
			require.NotNil(t, first[org], "what is missing at %s is installed", org)
		}
		pass()
		for _, org := range orgs {
			still := installed(org, module)
			require.Equal(t, first[org].GetId(), still.GetId(), "nothing is installed twice")
			require.Equal(t, first[org].GetRevision(), still.GetRevision(), "nor consented to again")
		}
	})

	t.Run("a composition install is recorded as the system's", func(t *testing.T) {
		inst := installed(fixtureOrganization, "conformance-composed")
		events, _, _, err := h.svc.QueryAuditLog(h.ctx, business.AuditQuery{OrgID: fixtureOrganization, EventType: string(business.EventInstallationCreated), PageSize: 500})
		require.NoError(t, err)
		i := slices.IndexFunc(events, func(e business.AuditEntry) bool { return e.ResourceID == inst.GetId() })
		require.GreaterOrEqual(t, i, 0, "the install is audited")
		require.Equal(t, business.ActorTypeSystem, events[i].ActorType)
		require.Empty(t, events[i].ActorID, "no person installed it")
		require.Equal(t, "composition", events[i].Payload["by"])
	})

	t.Run("a person never uninstalls a module", func(t *testing.T) {
		inst := installed(fixtureOrganization, "conformance-composed")
		err := h.svc.UninstallSolution(h.ctx, conformanceAlex, fixtureOrganization, inst.GetId())
		require.ErrorContains(t, err, "only its delivered withdrawal retires it")
		require.NotNil(t, installed(fixtureOrganization, "conformance-composed"), "it stays installed")
	})

	t.Run("an organization whose owner lost administration still gets its modules", func(t *testing.T) {
		// Two people of no other organization, so nobody else's session changes.
		const org, newcomer, steward, module = "00000000-0000-7000-8000-0000000002c2", "00000000-0000-7000-8000-0000000000e4", "00000000-0000-7000-8000-0000000000e5", "conformance-composed-later"
		_, err := h.svc.CreateFixtureOrganization(h.ctx, newcomer, &gen.CreateOrganizationRequest{Name: "Composition Owner Left"}, org)
		require.NoError(t, err)
		require.NoError(t, h.svc.AddOrgMember(h.ctx, newcomer, &gen.AddOrgMemberRequest{OrgId: org, UserId: steward, Role: gen.OrgRole_ORG_ROLE_ADMIN}))
		require.NoError(t, h.store.WithControlPlane(h.ctx, func(ctx context.Context) error {
			_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE public.organization_members SET role = 'member' WHERE org_id = $1 AND user_id = $2`, org, newcomer)
			return err
		}))
		deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{wireScope(module+".item", "read")}})
		inst := installed(org, module)
		require.NotNil(t, inst, "installed though the organization's owner is no administrator now")
		require.Equal(t, steward, inst.GetOwnerPrincipalId(), "its owner of record is a current administrator")
	})

	t.Run("a module page selecting a standing role is reported, never installed", func(t *testing.T) {
		const page = "conformance-composed-selecting"
		entry := wireScope(page+".entry", "read")
		deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: page, ceilings: []*authorityv1.WorkScopeV1{entry}, installationRole: "viewer",
			page:  &solutionhost.Frontend{Title: "Selecting page", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {entry}, "viewer": {entry}}})
		for _, org := range orgs {
			require.Nil(t, installed(org, page), "not installed at %s", org)
		}
		err := h.svc.KeepAuthorityCoreCurrent(h.ctx)
		require.ErrorContains(t, err, page)
		require.ErrorContains(t, err, `selects the standing role "viewer"`)
	})

	t.Run("a role-declaring module is installed this way and has no access until set up", func(t *testing.T) {
		const page = "conformance-composed-page"
		entry := wireScope(page+".entry", "read")
		deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: page, ceilings: []*authorityv1.WorkScopeV1{entry}, installationRole: domain.NoInstallationRole,
			page:  &solutionhost.Frontend{Title: "Composed page", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {entry}, "viewer": {entry}}})
		for _, org := range orgs {
			inst := installed(org, page)
			require.NotNil(t, inst, "the module page is installed at %s by composition", org)
			grants, err := h.core.ListRoleGrants(h.ctx, authority.Person{ID: map[string]string{fixtureOrganization: conformanceAlex, globexOrganization: conformanceZoe}[org], Organization: org}, org, inst.GetId())
			require.NoError(t, err)
			require.Empty(t, grants, "nobody holds a role on it until it is set up")
		}
	})

	t.Run("a set-up module page's grants are edited from its Setup page", func(t *testing.T) {
		const page = "conformance-composed-page"
		inst := installed(fixtureOrganization, page)
		require.NotNil(t, inst)
		sess, err := sessionv1.NewSessionServiceClient(h.conn).SignIn(h.ctx, &sessionv1.SignInRequest{FixtureToken: "fixture-alex"})
		require.NoError(t, err)
		alex := metadata.AppendToOutgoingContext(h.ctx, "authorization", "Bearer "+sess.GetBearer())
		client := installationv1.NewInstallationServiceClient(h.conn)
		// The Setup page reads the roles from the installation read, which
		// answers the module page's declared roles on its composition
		// installation; the offer read refuses a module.
		listed, err := client.List(alex, &installationv1.ListRequest{OrganizationId: fixtureOrganization})
		require.NoError(t, err)
		var roles []string
		for _, i := range listed.GetInstallations() {
			if i.GetId() == inst.GetId() {
				roles = i.GetRoles()
			}
		}
		require.Equal(t, []string{"installer", "viewer"}, roles)
		grant := &installationv1.GrantRoleRequest{OrganizationId: fixtureOrganization, InstallationId: inst.GetId(), Role: "viewer", PersonId: conformanceMaya}
		_, err = client.GrantRole(alex, grant)
		require.NoError(t, err)
		grants, err := client.ListRoleGrants(alex, &installationv1.ListRoleGrantsRequest{OrganizationId: fixtureOrganization, InstallationId: inst.GetId()})
		require.NoError(t, err)
		require.Len(t, grants.GetGrants(), 1)
		require.Equal(t, conformanceMaya, grants.GetGrants()[0].GetPersonId())
		_, err = client.RevokeRole(alex, &installationv1.RevokeRoleRequest{OrganizationId: fixtureOrganization, InstallationId: inst.GetId(), Role: "viewer", PersonId: conformanceMaya})
		require.NoError(t, err)
		grants, err = client.ListRoleGrants(alex, &installationv1.ListRoleGrantsRequest{OrganizationId: fixtureOrganization, InstallationId: inst.GetId()})
		require.NoError(t, err)
		require.Empty(t, grants.GetGrants())
	})

	t.Run("a page's binding is told every composed module without roles, never a module page", func(t *testing.T) {
		read := func(include bool, token string) *gen.ListSolutionEntitlementsResponse {
			t.Helper()
			out, err := h.svc.ListSolutionEntitlements(h.ctx, &gen.ListSolutionEntitlementsRequest{OrgId: fixtureOrganization, SubjectId: conformanceMaya, IncludeComposedModules: include, PageToken: token})
			require.NoError(t, err)
			return out
		}
		composed := map[string]string{}
		for _, c := range read(true, "").GetComposedModules() {
			composed[c.GetTargetId()] = c.GetInstallationId()
		}
		for _, module := range []string{"conformance-composed", "documents"} {
			inst := installed(fixtureOrganization, module)
			require.Equal(t, inst.GetId(), composed[inst.GetTargetId()], "%s, declaring no roles, is named by its composition installation", module)
		}
		page := installed(fixtureOrganization, "conformance-composed-page")
		require.NotContains(t, composed, page.GetTargetId(), "a module declaring roles is reached by grant alone")
		for _, e := range read(true, "").GetEntitlements() {
			require.NotContains(t, composed, e.GetTargetId(), "naming a composed module entitles nobody to it")
		}
		require.Empty(t, read(false, "").GetComposedModules(), "the navigation and the proxy do not ask, and are not answered")
		require.Empty(t, read(true, page.GetId()).GetComposedModules(), "a later page carries none")
	})

	t.Run("withdrawal revokes in every organization", func(t *testing.T) {
		const module = "conformance-composed"
		ids := map[string]string{}
		for _, org := range orgs {
			ids[org] = installed(org, module).GetId()
		}
		withdrawModule(t, h.ctx, h.conn, h.signer, module, 2)
		for _, org := range orgs {
			read, _, err := h.svc.GetInstallation(h.ctx, org, ids[org])
			require.NoError(t, err)
			require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_REVOKED, read.GetStatus(), "the withdrawn module's installation at %s is revoked", org)
		}
	})
}

// withdrawModule delivers module's withdrawal at generation, as the CLI does.
func withdrawModule(t *testing.T, ctx context.Context, conn grpc.ClientConnInterface, signer *solutionhost.InMemorySigner, module string, generation uint64) {
	t.Helper()
	manifest := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: module, Alias: module, Kind: solutionhost.KindModule, Generation: generation,
		Host: solutionhost.HostTarget{Coordinate: "conformance-host", Domain: "acme"}, Release: solutionhost.Release{Publisher: "conformance", Name: module, Version: "0.1.0"}, Removed: true}
	require.NoError(t, manifest.Validate())
	raw, err := solutionhost.SignedPayload(manifest)
	require.NoError(t, err)
	_, err = compositionv1.NewCompositionServiceClient(conn).ApplyManifest(ctx, &compositionv1.ApplyManifestRequest{
		Carrier: &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, signer.KeyID())}, Credential: "delivery:" + module})
	require.NoError(t, err)
}
