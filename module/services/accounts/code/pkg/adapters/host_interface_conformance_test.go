package adapters

// The harness: accounts, booted over its test store with its fixture, serves
// the host interfaces over its Connect listener and the shared conformance
// suite (codefly-dev/authority libraries/conformance) drives them, then the
// solution-principal target and the cases beyond the suite. The suite is never
// loosened to make a line green.

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	conformance "github.com/codefly-dev/authority/libraries/conformance/go"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/solutionhost"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"accounts/fixtures"
	"accounts/internal/deliverytest"
	"accounts/pkg/auth"
	Package "accounts/pkg/auth/dev"
	ed25519minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	artifactv1 "accounts/pkg/gen/host/artifact/v1"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
)

// fixtureOrganization is Acme Corp in the harness's fixture, globexOrganization
// Globex, and conformanceAlex one of Acme's administrators.
const (
	fixtureOrganization = "00000000-0000-7000-8000-0000000000c1"
	globexOrganization  = "00000000-0000-7000-8000-0000000000c2"
	conformanceAlex     = "00000000-0000-7000-8000-0000000000b3"
	conformanceAdmin    = "00000000-0000-7000-8000-0000000000a2"
	conformanceIvy      = "00000000-0000-7000-8000-0000000000b9"
	conformanceZoe      = "00000000-0000-7000-8000-0000000000b5"
)

// TestAccountsPassesTheHostInterfaceConformanceSuite boots accounts over a
// fresh test store, seeds the harness's fixture and runs the shared host
// interface conformance suite against it, then the cases beyond the suite.
// It runs wherever the package's tests run with their dependencies, as every
// other store-backed test does: a switch to turn it on was a switch nothing in
// CI turned, so the suite was skipped there. It includes everything the
// fresh-store test it replaces asserted (manifest authority derived from an
// empty store, a person issued).
func TestAccountsPassesTheHostInterfaceConformanceSuite(t *testing.T) {
	runHostInterfaceFixture(t)
}

func runHostInterfaceFixture(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// The SDK starts its CLI with this context. Its lifetime must extend past
	// service cancellation so Destroy can stop the dependencies through the CLI.
	dependencyCtx, stopDependencies := context.WithCancel(context.WithoutCancel(ctx))
	t.Cleanup(stopDependencies)
	deps, err := sdk.WithDependencies(dependencyCtx, sdk.WithExcludedDependencies("cache"), sdk.WithNamingScope("host-interface-conformance"), sdk.WithTimeout(10*time.Minute), sdk.WithSilence("store"))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cleanupCancel()
		if err := deps.Destroy(cleanupCtx); err != nil {
			t.Errorf("destroy the conformance dependencies: %v", err)
		}
	})
	_, err = codefly.Init(ctx)
	require.NoError(t, err)
	store, err := infra.NewPostgresStore(ctx)
	require.NoError(t, err)
	t.Cleanup(store.Close)
	svc, err := business.NewService(store)
	require.NoError(t, err)
	// The sign-in path, as production and the business tests wire it: a session
	// store and an identity resolver over the store, a token minter with a test
	// key, the audit emitter and the entitlement checker every audited RPC needs.
	sessionStore := pgauth.NewSessionStore(store)
	svc.SetIdentityResolver(pgauth.NewResolver(store))
	_, minterKey, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	minter := ed25519minter.New(ed25519minter.Config{Issuer: "accounts-conformance", Audience: "accounts-conformance"}, minterKey, sessionStore)
	// Production revokes through the cache (redisstate.TokenRevoker), which this
	// harness does not boot; an in-memory list stands in at the same seam, so a
	// sign-out is refused by the perimeter exactly as it is there.
	minter.SetRevoker(&memoryRevoker{})
	svc.SetJWTMinter(minter)
	auditEmitter, err := business.NewDurableAuditEmitter(store, store)
	require.NoError(t, err)
	t.Cleanup(auditEmitter.Close)
	svc.SetAuditEmitter(auditEmitter)
	svc.SetEntitlementChecker(business.NewDefaultEntitlementChecker(store, business.NoPlansSold))
	require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
		return store.SyncAuditEventTypes(ctx, business.AuditEventCatalog())
	}))

	// The people: the harness's fixture (the dev-admin people, with the roles,
	// assignments and installations the suite needs), signed in through the
	// fixture provider and seeded through the fixture path production runs.
	t.Setenv("DEV_FIXTURE_PATH", conformanceFixturePath(t))
	fixturePath, err := fixtures.FixturePath(conformanceFixtureName)
	require.NoError(t, err)
	validator, err := Package.New(fixturePath)
	require.NoError(t, err)
	svc.SetDevelopmentTokenValidator(validator)
	installations, err := fixtures.SeedWithInstallations(ctx, svc, conformanceFixtureName)
	require.NoError(t, err)

	// The cluster: a fake TokenReview projects the credential a pod of each
	// declared workload presents (namespace and service account are the
	// module's alias, as the namespace rule gives them with no prefix).
	cluster := executiontest.New()
	svc.SetExecutionBinding(cluster)
	credential := func(name string) string {
		return cluster.Run(executiontest.Workload{Namespace: name, ServiceAccount: name, Container: "app"})
	}

	// Signed delivery uses the production inbox, verifier and reconciler: the
	// composition's ApplyManifest records each delivery in accounts' delivery
	// owner and composes its module in the authority core.
	signer := deliverytest.Signer(t, "conformance-release")
	verifier := deliverytest.Verifier(t, map[string][]string{signer.KeyID(): {"acme"}})
	svc.SetSolutionDelivery(verifier, store, conformanceCarrier{})
	svc.SetSolutionDeliveryCoordinate("conformance-host")
	svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
	})
	svc.SetModuleEnvelopeStore(store)
	svc.SetAdmissionKeyStore(store)
	reconciler, err := business.NewSolutionHostBindingReconciler(svc, business.SolutionHostBindingReconcilerConfig{
		Source: business.NewDeliveredSolutionHostBindings(svc), Verifier: verifier, Coordinate: "conformance-host",
	})
	require.NoError(t, err)
	svc.SetCompositionReconcile(reconciler.RunOnce)
	previous := service
	service = svc
	t.Cleanup(func() { service = previous })

	// The servers, as production builds them; then the Work Context key and the
	// authority core configured against this store with a test key, as
	// configurePermissionServerKeys does from the permissions plugin.
	grpcServer, err := NewGrpServer(&Configuration{})
	require.NoError(t, err)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authorityStore, _ := svc.Store().(business.WorkContextAuthorityStore)
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{
		Issuer: "https://accounts.conformance.test", KeyID: "conformance-1", PrivateKey: key, Authority: authorityStore, Seals: svc,
	})
	require.NoError(t, workContextSingleton.configureErr)
	require.NoError(t, svc.ConfigureAuthorityCore(ctx, "https://accounts.conformance.test", "conformance-1", key))
	core, err := svc.AuthorityCore()
	require.NoError(t, err)

	// The Connect listener is the catalog-complete one and speaks the gRPC
	// protocol over h2c, so the suite dials it with a plain gRPC client.
	port := serveConnect(t, ctx, grpcServer)
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// The composition, delivered as the CLI delivers it. documents is the
	// audience the viewer reads at; org-directory the page module reading
	// documents for the viewer through its one binding; model an audience the
	// viewer's context at documents is not addressed to. documents and model
	// bind to each other, so a delegate chain crosses from one to the other and
	// back; model also binds to itself across both its kinds. conformance-agent
	// is a module whose tools binding reaches an installed solution;
	// conformance-idle a declared workload with no active installation.
	modules := []conformanceModule{
		{alias: "documents", ceilings: []*authorityv1.WorkScopeV1{wireScope("example-records", "invoke", "read", "write", "ingest")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.profiles:invoke", "modelservice.profiles:read"}}}}},
		{alias: "org-directory", ceilings: []*authorityv1.WorkScopeV1{wireScope("directory.entry", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "documents", Audience: "documents", Operations: map[string][]string{
				"invoke": {"example-records:invoke", "example-records:read"}, "lookup": {"example-records:read"}, "headless": {"example-records:invoke", "example-records:read"}}}}},
		{alias: "model", ceilings: []*authorityv1.WorkScopeV1{wireScope("modelservice.profiles", "invoke", "read"), wireScope("modelservice.datasets", "read")},
			bindings: []solutionhost.AuthorityBinding{
				{ID: "records", Audience: "documents", Operations: map[string][]string{"invoke": {"example-records:invoke", "example-records:read"}, "lookup": {"example-records:read"}}},
				{ID: "self", Audience: "model", Operations: map[string][]string{
					"invoke":   {"modelservice.profiles:invoke", "modelservice.profiles:read", "modelservice.datasets:read"},
					"headless": {"modelservice.profiles:invoke", "modelservice.profiles:read", "modelservice.datasets:read"}}},
			}},
		{alias: "conformance-agent", ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-agent.conversation", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "tools", Audience: "conformance-proposals", Operations: map[string][]string{"invoke": {"conformance-proposals.item:read"}}}}},
		{alias: "conformance-idle", ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-idle.task", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.profiles:read"}, "headless": {"modelservice.profiles:read"}}}}},
	}
	for _, m := range modules {
		deliverModule(t, ctx, conn, signer, m)
	}
	// installModule is how a module comes to be installed here: Acme's and
	// Globex's administrators each install it, through accounts' own path.
	installModule := func(ctx context.Context, module string) error {
		if err := consentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, module); err != nil {
			return err
		}
		return consentModule(ctx, svc, store, globexOrganization, conformanceZoe, module)
	}
	// Every module but conformance-idle is installed at both organizations by
	// their administrators, through accounts' own path: the core installs no
	// module itself. conformance-idle stays a declared workload with no
	// installation anywhere, as the suite's absent-organization case needs.
	for _, m := range modules {
		if m.alias == "conformance-idle" {
			continue
		}
		require.NoError(t, installModule(ctx, m.alias))
	}

	// The solutions, delivered as the CLI delivers them, then installed by the
	// fixture's declared installations through the core. conformance-pages
	// carries the roles, routes and binding the suite's solution-role cases
	// deliver at the next generation.
	pagesViewer := []*authorityv1.WorkScopeV1{wireScope("conformance-pages.page", "read"), wireScope("example-records", "write")}
	for _, solution := range []conformanceSolution{{
		alias: "conformance-pages", generation: 1,
		ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-pages.page", "read", "write", "publish")},
		roles: map[string][]*authorityv1.WorkScopeV1{
			"installer": {wireScope("conformance-pages.page", "read", "write", "publish"), wireScope("example-records", "write")},
			"editor":    {wireScope("conformance-pages.page", "read", "write")},
			"viewer":    pagesViewer,
		},
		methods: []*compositionv1.MethodPolicyV1{
			{Method: "GET /items/{id}", Requires: pagesViewer[:1]},
			{Method: "POST /items/{id}", Requires: []*authorityv1.WorkScopeV1{wireScope("conformance-pages.page", "write")}},
			{Method: "GET /open/{path=**}", AnyCheckedCaller: true},
		},
		bindings: []solutionhost.AuthorityBinding{{ID: "documents", Audience: "documents", Operations: map[string][]string{"invoke": {"example-records:write"}}}},
	}, {
		// What a module's tools binding reaches: finance is entitled, engineering is not.
		alias: "conformance-proposals", generation: 1,
		ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-proposals.item", "read", "write")},
		roles: map[string][]*authorityv1.WorkScopeV1{
			"installer": {wireScope("conformance-proposals.item", "read", "write")},
			"viewer":    {wireScope("conformance-proposals.item", "read")},
		},
		methods: []*compositionv1.MethodPolicyV1{{Method: "/tools.Solution/Read", Requires: []*authorityv1.WorkScopeV1{wireScope("conformance-proposals.item", "read")}}},
	}, {
		// An installation that acts for itself down a chain of three modules.
		alias: "conformance-assistant", generation: 1,
		ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-assistant.page", "read")},
		roles: map[string][]*authorityv1.WorkScopeV1{
			"installer": {wireScope("conformance-assistant.page", "read")},
			"viewer": {wireScope("conformance-assistant.page", "read"), wireScope("directory.entry", "read"),
				wireScope("example-records", "read"), wireScope("modelservice.profiles", "invoke", "read")},
		},
		bindings: []solutionhost.AuthorityBinding{
			{ID: "directory", Audience: "org-directory", Operations: map[string][]string{"invoke": {"directory.entry:read"}}},
			{ID: "documents", Audience: "documents", Operations: map[string][]string{"invoke": {"example-records:read"}}},
			{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.profiles:invoke", "modelservice.profiles:read"}}},
		},
	}} {
		deliverSolution(t, ctx, conn, signer, solution)
	}
	// Two installed solutions for installation-owned role scopes: Alex installs
	// both, and Maya's finance team holds A's viewer role and nothing of B's.
	for _, owned := range []string{"conformance-owner-a", "conformance-owner-b"} {
		note := wireScope(owned+".note", "read")
		deliverSolution(t, ctx, conn, signer, conformanceSolution{alias: owned, generation: 1, ceilings: []*authorityv1.WorkScopeV1{note},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {note}, "viewer": {note}}})
	}
	require.NoError(t, installations.ConvergeAuthority(ctx), "every declared installation converges once its target is delivered")
	installed := func(target string) string { return authority.StableInstallationID(fixtureOrganization, target) }
	proposals, assistant := installed("conformance-proposals"), installed("conformance-assistant")
	finance := teamNamed(t, ctx, svc, fixtureOrganization, "Finance")
	// Ivy, a member and no administrator, installs both owned solutions as the
	// host's setup and holds their installer role; Maya's finance team holds
	// A's viewer role and nothing of B's.
	for _, owned := range []string{"conformance-owner-a", "conformance-owner-b"} {
		_, err := core.InstallAsHost(ctx, fixtureOrganization, owned, "viewer", conformanceIvy, nil)
		require.NoError(t, err)
		_, err = core.GrantRoleAsHost(ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: installed(owned), Role: domain.InstallerRole, PersonID: conformanceIvy})
		require.NoError(t, err)
	}
	_, err = core.GrantRoleAsHost(ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: installed("conformance-owner-a"), Role: "viewer", TeamID: finance})
	require.NoError(t, err)

	documentsCredential, modelCredential, workloadCredential := credential("documents"), credential("model"), credential("org-directory")
	// A delegate chain: an administrator issued at documents; documents hops to
	// model through its binding for what the first token never carried, then
	// model hops back to documents through its own.
	chain := conformance.Chain{Token: "fixture-alex", Start: "documents", Hops: []conformance.Hop{
		{Credential: documentsCredential, Binding: "model", Audience: "model", AskKind: "modelservice.profiles", AskAction: "invoke"},
		{Credential: modelCredential, Binding: "records", Audience: "documents", AskKind: "example-records", AskAction: "read"},
	}}
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization, SessionID: "conformance-harness"}
	conformance.Run(t, conn, registeredDescriptors(t), conformance.Fixture{
		Organization:   fixtureOrganization,
		HostCoordinate: "conformance-host", HostDomain: "acme",
		SignManifest:       func(doc []byte) ([]byte, error) { return solutionhost.SignEnvelope(doc, signer) },
		SignAuthority:      func(doc []byte) ([]byte, error) { return delivery.Sign(doc, signer.KeyID(), signer.Sign) },
		DeliveryCredential: func(module string) string { return "delivery:" + module },
		InstallModule:      installModule,
		// accounts excludes OfferService (interface_policies.go), so the
		// declaration case compares the reviewed offer on the local authority
		// alone until accounts serves it; its grants run here as everywhere.
		ReviewOfferNotServed: "accounts does not serve host.installation.v1.OfferService (excluded in pkg/business/interface_policies.go)",
		ViewerToken:          "dev-bob", AdminToken: "dev-alice",
		Audience: "documents", ReadKind: "example-records",
		WorkloadModule: "org-directory", WorkloadCredential: workloadCredential, Binding: "documents", BindingAudience: "documents", Elsewhere: "model",
		// The installed proposals solution and its own workload. Its roles name
		// its own kind alone, so documents (Audience) is unrelated to it.
		InstalledSolution: proposals, InstalledAudience: "conformance-proposals", InstalledCredential: credential("conformance-proposals"),
		// An Acme owner who is not an administrator; a member with no role and no team.
		OwnerToken: "fixture-owner", UnentitledToken: "fixture-no-roles",
		// Globex's administrator, and the hop's own workload credential (service
		// account hop in the authority module's namespace).
		OtherAdminToken: "fixture-zoe",
		HopCredential:   cluster.Run(executiontest.Workload{Namespace: business.HopModule, ServiceAccount: business.HopService, Container: "app"}),
		// Maya, on finance, views the pages; Nora holds no role there. The
		// viewer role writes example records at documents through the
		// solution's binding, never on its own.
		SolutionRoles: &conformance.SolutionRoleCase{
			Solution: "conformance-pages", Credential: credential("conformance-pages"), Token: "fixture-maya", Outsider: "fixture-no-roles",
			OwnKind: "conformance-pages.page", OwnActions: []string{"read", "write", "publish"},
			Binding: "documents", Audience: "documents", ConsumedKind: "example-records", ConsumedAction: "write", ForbiddenAction: "ingest", Generation: 2,
		},
		// Both Maya and Remi use the agent; only Maya's team holds the proposals.
		SolutionTools: &conformance.SolutionToolsCase{Token: "fixture-maya", Outsider: "fixture-remi", Module: "conformance-agent", Credential: credential("conformance-agent"),
			Binding: "tools", Audience: "conformance-proposals", Kind: "conformance-proposals.item", Action: "read", Method: "/tools.Solution/Read"},
		// org-directory is installed at both organisations; conformance-idle at none.
		WorkloadOrganizations: &conformance.WorkloadOrganizationsCase{
			Ambiguous: conformance.WorkloadOrganizationCase{Credential: workloadCredential, Module: "org-directory", Binding: "documents", Audience: "documents"},
			None:      conformance.WorkloadOrganizationCase{Credential: credential("conformance-idle"), Module: "conformance-idle", Binding: "model", Audience: "model"},
		},
		// Maya reads one example record by grant, not the kind.
		Resources: &conformance.ResourceCase{Token: "fixture-maya", Audience: "documents", Kind: "example-records", Action: "read",
			Covered: []string{"conformance-record-a"}, Uncovered: []string{"conformance-record-b"}, Unheld: "write"},
		// Alex lets the directory page invoke example records for him; Bob, a
		// reader, cannot delegate what he does not hold.
		Delegation: &conformance.DelegationCase{Token: "fixture-alex", Outsider: "dev-bob", Module: "org-directory", Credential: workloadCredential,
			Binding: "documents", Audience: "documents", Kind: "example-records", Action: "invoke"},
		// Alex's chain below, projected through each target binding.
		ChildScopes: &conformance.ChildScopesCase{Chain: chain, FirstActions: []string{"invoke", "read"}, NextActions: []string{"invoke", "read"},
			OutsideKind: "example-records", OutsideAction: "read", ResourceID: "a", OtherResourceID: "b", DroppedAction: "invoke"},
		// Alex at model, out to documents and back, keeps what the root narrowed.
		ParentLimits: &conformance.ParentLimitsCase{Token: "fixture-alex", Root: "model", Credential: modelCredential, SelfBinding: "self",
			Kind: "modelservice.profiles", DroppedKind: "modelservice.datasets", Action: "read", DroppedAction: "invoke", ResourceID: "a", OtherResourceID: "b",
			Out:  conformance.Hop{Credential: modelCredential, Audience: "documents", Binding: "records"},
			Back: conformance.Hop{Credential: documentsCredential, Audience: "model", Binding: "model"}},
		// The assistant's installation, acting for itself through three modules.
		InstallationChain: &conformance.InstallationChainCase{Installation: assistant, Source: "conformance-assistant", Credential: credential("conformance-assistant"),
			Hops: []conformance.Hop{
				{Credential: credential("conformance-assistant"), Binding: "directory", Audience: "org-directory", AskKind: "directory.entry", AskAction: "read"},
				{Credential: workloadCredential, Binding: "documents", Audience: "documents", AskKind: "example-records", AskAction: "read"},
				{Credential: documentsCredential, Binding: "model", Audience: "model", AskKind: "modelservice.profiles", AskAction: "invoke"},
			}},
		// Two installed solutions, a store module and five resources; Ivy
		// administers collection-x alone through her own organization grant.
		InstallationResources: &conformance.InstallationResourcesCase{
			Owner: "fixture-ivy", Viewer: "fixture-maya", Generation: 2,
			A:     conformance.OwnedInstallation{Solution: "conformance-owner-a", Credential: credential("conformance-owner-a"), Installation: installed("conformance-owner-a")},
			B:     conformance.OwnedInstallation{Solution: "conformance-owner-b", Credential: credential("conformance-owner-b"), Installation: installed("conformance-owner-b")},
			Store: "conformance-store", StoreCredential: credential("conformance-store"), Kind: "conformance.collection",
			ResourceA: "collection-a", ResourceA2: "collection-a2", ResourceB: "collection-b", ResourceX: "collection-x", ResourceY: "collection-y",
		},
		// The suite delivers conformance-lifecycle and installs it; its step
		// waits until the harness resumes it, and uninstalling revokes it.
		Lifecycle: &conformance.InstallationLifecycleCase{
			Target: "conformance-lifecycle", Credential: credential("conformance-lifecycle"), Kind: "conformance-lifecycle.page", Team: finance,
			SetAdministrator: func(ctx context.Context, administrator bool) error {
				role := gen.OrgRole_ORG_ROLE_MEMBER
				if administrator {
					role = gen.OrgRole_ORG_ROLE_ADMIN
				}
				return svc.ConvergeFixtureOrgMember(ctx, &gen.AddOrgMemberRequest{
					OrgId: fixtureOrganization, UserId: conformanceAdmin, Role: role,
				})
			},
			PauseInstall: func() func() {
				release := make(chan struct{})
				svc.SetAuthorityInstallStep(func(ctx context.Context, _ domain.Installation) error {
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
				var once sync.Once
				return func() { once.Do(func() { close(release); svc.SetAuthorityInstallStep(nil) }) }
			},
			Revoke: func(ctx context.Context, id string) error { return core.Uninstall(ctx, alex, fixtureOrganization, id) },
		},
		Chain: &chain,
		// The same binding read as a lookup carries its lookup scopes alone.
		Lookup: &conformance.LookupCase{Token: "fixture-alex", Start: "model", Credential: modelCredential, Binding: "records",
			Audience: "documents", Kind: "example-records", Invoke: "invoke", Lookup: "read"},
		// The permission matrix: a collection reader, Acme administrators, a
		// member who holds no role there, and a Globex member at Acme's content.
		Matrix: []conformance.Expectation{
			{Token: "dev-bob", Audience: "documents", Kind: "example-records", Action: "read", Allowed: true},
			{Token: "dev-bob", Audience: "documents", Kind: "example-records", Action: "write", Allowed: false},
			{Token: "dev-alice", Audience: "documents", Kind: "example-records", Action: "write", Allowed: true},
			{Token: "fixture-alex", Audience: "model", Kind: "modelservice.profiles", Action: "invoke", Allowed: true},
			{Token: "fixture-maya", Audience: "model", Kind: "modelservice.profiles", Action: "invoke", Allowed: false},
			{Token: "fixture-omar", Audience: "documents", Kind: "example-records", Action: "read", Allowed: false},
		},
	})

	// The solution-principal target (conformance RunSolutionPrincipalTarget):
	// stable solution principals, agent-role isolation, owner-aware audiences,
	// own and foreign data, role administration and subject-specific
	// revocation, membership loss, transfer, uninstall and reinstall.
	//
	// A person leaving a team or the organization goes through accounts' own
	// production paths (RemoveTeamMember, RemoveOrgMember), as an Acme
	// administrator: the narrowing applies directly, its audit record in the
	// same transaction, and the suite proves the next issue no longer carries
	// what the membership gave.
	removedFromTeam, removedFromOrganization := map[string]string{}, map[string]bool{}
	// The organization leaver's session, signed in while still a member.
	leaverSession, err := sessionv1.NewSessionServiceClient(conn).SignIn(ctx, &sessionv1.SignInRequest{FixtureToken: "fixture-principal-org-leaver"})
	require.NoError(t, err)
	conformance.RunSolutionPrincipalTarget(t, conn, registeredDescriptors(t), conformance.SolutionPrincipalCase{
		Organization: fixtureOrganization, OtherOrganization: globexOrganization, Generation: 1,
		A:     conformance.SolutionUnderTest{Solution: "principal-a", Credential: credential("principal-a")},
		B:     conformance.SolutionUnderTest{Solution: "principal-b", Credential: credential("principal-b")},
		C:     conformance.SolutionUnderTest{Solution: "principal-c", Credential: credential("principal-c")},
		Store: "principal-store", StoreCredential: credential("principal-store"), Kind: "principal.collection",
		LegacyStore: "principal-legacy-store", LegacyStoreCredential: credential("principal-legacy-store"), LegacyKind: "principal.legacy",
		Admin: "fixture-alex", Viewer: "fixture-maya", Editor: "fixture-remi", Member: "fixture-no-roles", Installer: "fixture-principal-installer",
		Team: teamNamed(t, ctx, svc, fixtureOrganization, "Principal team"), TeamMember: "fixture-principal-team-member", TeamLeaver: "fixture-principal-team-leaver",
		OrgLeaver: "fixture-principal-org-leaver", Administrator: "fixture-store-administrator", Reader: "fixture-store-reader", NoteReader: "fixture-principal-note-reader",
		OtherAdmin: "fixture-zoe", OtherMember: "fixture-omar", OtherTeam: teamNamed(t, ctx, svc, globexOrganization, "Ops"),
		Existing:  "store-x",
		Uninstall: func(ctx context.Context, id string) error { return core.UninstallAsHost(ctx, fixtureOrganization, id) },
		RemoveFromTeam: func(ctx context.Context, person, team string) error {
			removedFromTeam[person] = team
			return svc.RemoveTeamMember(ctx, conformanceAlex, &gen.RemoveTeamMemberRequest{TeamId: team, UserId: person})
		},
		RemoveFromOrganization: func(ctx context.Context, person string) error {
			removedFromOrganization[person] = true
			return svc.RemoveOrgMember(ctx, conformanceAlex, &gen.RemoveOrgMemberRequest{OrgId: fixtureOrganization, UserId: person})
		},
		SignManifest:       func(doc []byte) ([]byte, error) { return solutionhost.SignEnvelope(doc, signer) },
		SignAuthority:      func(doc []byte) ([]byte, error) { return delivery.Sign(doc, signer.KeyID(), signer.Sign) },
		DeliveryCredential: func(module string) string { return "delivery:" + module },
		InstallModule:      installModule,
		HostCoordinate:     "conformance-host", HostDomain: "acme",
	})

	signIn := func(token string) context.Context {
		t.Helper()
		sess, err := sessionv1.NewSessionServiceClient(conn).SignIn(ctx, &sessionv1.SignInRequest{FixtureToken: token})
		require.NoError(t, err)
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+sess.GetBearer())
	}
	requireCode := func(t *testing.T, err error, code codes.Code, reason string) {
		t.Helper()
		require.Equal(t, code, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), reason)
	}
	records := func(actions ...string) []*authorityv1.WorkScopeV1 {
		return []*authorityv1.WorkScopeV1{{ResourceKind: "example-records", Actions: actions}}
	}

	// Each removal the target made is in accounts' audit journal, naming the
	// person removed and the administrator who removed them.
	t.Run("a membership removal applies at once and is recorded in the audit journal", func(t *testing.T) {
		require.NotEmpty(t, removedFromTeam, "the target removed a team member")
		require.NotEmpty(t, removedFromOrganization, "the target removed an organization member")
		removals := func(event, resource, person string) int {
			var count int
			require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
				return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM public.audit_events
					WHERE event_type = $1 AND resource_id = $2 AND actor_id = $3 AND payload->>'user_id' = $4`,
					event, resource, conformanceAlex, person).Scan(&count)
			}))
			return count
		}
		for person, team := range removedFromTeam {
			require.Equal(t, 1, removals(string(business.EventTeamMemberRemoved), team, person), "team removal of %s", person)
		}
		for person := range removedFromOrganization {
			require.Equal(t, 1, removals(string(business.EventOrgMemberRemoved), fixtureOrganization, person), "organization removal of %s", person)
		}
		// And the session the organization leaver signed in with while a member
		// is issued nothing there any more.
		leaver := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+leaverSession.GetBearer())
		_, err := authorityv1.NewAuthorityServiceClient(conn).Issue(leaver, &authorityv1.IssueRequest{
			As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: "documents", Scopes: records("read")})
		requireCode(t, err, codes.PermissionDenied, "not a member of this organization")
	})

	// Beyond the suite: a delegation never reaches past what its grantor holds
	// or what it names; once revoked, by its grantor or an administrator of its
	// organization, it issues nothing and every capability issued from it fails
	// its next check. Anyone else learns nothing of it.
	// A module is installed at an organization only by its administrator's
	// consent, through accounts' own path: its workload is issued nothing at an
	// organization that did not install it, nothing past the organization the
	// module envelope pins it to, and nothing once an administrator uninstalls
	// it, when what it was issued fails its next check.
	t.Run("a module workload is issued only where it is installed, within its pin, until uninstalled", func(t *testing.T) {
		client := authorityv1.NewAuthorityServiceClient(conn)
		const module = "conformance-consent"
		deliverModule(t, ctx, conn, signer, conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-consent.task", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"headless": {"modelservice.profiles:read"}}}}})
		workloadOf := credential(module)
		issue := func(org string) (*authorityv1.IssueResponse, error) {
			return client.Issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Workload{Workload: &authorityv1.WorkloadCredential{Credential: workloadOf}},
				OperationBindingId: "model", Audience: "model", OrganizationId: org})
		}
		_, err := issue(fixtureOrganization)
		requireCode(t, err, codes.FailedPrecondition, "is not installed at "+fixtureOrganization)

		require.NoError(t, consentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, module))
		issued, err := issue(fixtureOrganization)
		require.NoError(t, err, "installed by Acme's administrator")
		require.Equal(t, fixtureOrganization, issued.GetContext().GetTenantId())
		_, err = issue(globexOrganization)
		requireCode(t, err, codes.FailedPrecondition, "is not installed at "+globexOrganization)

		// Pinned to Acme by the module envelope, the workload is issued nothing
		// at Globex even once Globex installs it.
		require.NoError(t, consentModule(ctx, svc, store, globexOrganization, conformanceZoe, module))
		_, err = issue(globexOrganization)
		require.NoError(t, err, "installed by Globex's administrator")
		registry := svc.ModulePrincipals()
		pinned := registry[business.ModulePrincipalID(module)]
		pinned.Tenant = fixtureOrganization
		pinnedRegistry := svc.ModulePrincipals()
		pinnedRegistry[business.ModulePrincipalID(module)] = pinned
		svc.SetModulePrincipals(pinnedRegistry)
		_, err = issue(globexOrganization)
		svc.SetModulePrincipals(registry)
		requireCode(t, err, codes.PermissionDenied, "is bound to organization "+fixtureOrganization)

		// Acme's administrator uninstalls it: nothing more is issued there, and
		// what was issued fails its next check.
		installed, err := core.Installations(ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		var id string
		for _, inst := range installed {
			if inst.Target == module {
				id = inst.ID
			}
		}
		require.NotEmpty(t, id)
		require.NoError(t, svc.UninstallSolution(ctx, conformanceAlex, fixtureOrganization, id))
		_, err = issue(fixtureOrganization)
		requireCode(t, err, codes.FailedPrecondition, "is not installed at "+fixtureOrganization)
		_, err = client.Check(ctx, &authorityv1.CheckRequest{Token: issued.GetToken(), Audience: "model"})
		require.Error(t, err, "a capability issued before the uninstall fails its check")
	})

	// What an organization's administrator consented to bounds every issue
	// through the module's bindings there, exactly as the native mint is
	// bounded: an installation narrowed to no capability issues nothing, a
	// binding dropped from the consent (or from the module's grant within the
	// envelope) issues nothing, and a redelivery that widens a binding issues
	// no more than was consented. Capabilities issued before a narrowing fail
	// their next check.
	t.Run("a module workload is issued only what its installation's consent allows", func(t *testing.T) {
		client := authorityv1.NewAuthorityServiceClient(conn)
		const module = "conformance-narrow"
		narrow := conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-narrow.task", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"headless": {"modelservice.profiles:read"}}}}}
		deliverModule(t, ctx, conn, signer, narrow)
		workloadOf := credential(module)
		issue := func(org string) (*authorityv1.IssueResponse, error) {
			return client.Issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Workload{Workload: &authorityv1.WorkloadCredential{Credential: workloadOf}},
				OperationBindingId: "model", Audience: "model", OrganizationId: org})
		}
		installationAt := func(caller authority.Person, org string) string {
			t.Helper()
			installed, err := core.Installations(ctx, caller, org)
			require.NoError(t, err)
			for _, inst := range installed {
				if inst.Target == module {
					return inst.ID
				}
			}
			t.Fatalf("%s is not installed at %s", module, org)
			return ""
		}
		zoe := authority.Person{ID: conformanceZoe, Organization: globexOrganization, SessionID: "conformance-harness"}

		// Acme narrows its installation to no capability.
		require.NoError(t, consentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, module))
		before, err := issue(fixtureOrganization)
		require.NoError(t, err)
		acme := installationAt(alex, fixtureOrganization)
		require.NoError(t, svc.NarrowInstallation(ctx, conformanceAlex, fixtureOrganization, acme, business.InstallationScope{Capabilities: false}))
		_, err = client.Check(ctx, &authorityv1.CheckRequest{Token: before.GetToken(), Audience: "model"})
		require.Error(t, err, "a capability issued before the narrowing fails its check")
		_, err = svc.ModuleAuthorizeOperationContext(ctx, module, workloadOf, "model", fixtureOrganization)
		require.Error(t, err, "the native mint refuses")
		_, err = issue(fixtureOrganization)
		requireCode(t, err, codes.FailedPrecondition, "is not installed at "+fixtureOrganization)

		// Globex keeps capabilities but drops the binding from its consent.
		require.NoError(t, consentModule(ctx, svc, store, globexOrganization, conformanceZoe, module))
		_, err = issue(globexOrganization)
		require.NoError(t, err, "consented at Globex")
		globex := installationAt(zoe, globexOrganization)
		require.NoError(t, svc.NarrowInstallation(ctx, conformanceZoe, globexOrganization, globex, business.InstallationScope{Capabilities: true, Resources: []string{"conformance-narrow.task"}}))
		_, err = svc.ModuleAuthorizeOperationContext(ctx, module, workloadOf, "model", globexOrganization)
		require.Error(t, err, "the native mint refuses")
		_, err = issue(globexOrganization)
		requireCode(t, err, codes.PermissionDenied, `operation binding "model" is not one its installation at `+globexOrganization+" consents to")

		// A redelivery that widens the binding issues no more than Acme's
		// administrator consents to when they install it again.
		widened := narrow
		widened.generation = 2
		widened.bindings = []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{
			"headless": {"modelservice.profiles:read", "modelservice.datasets:read"}}}}
		const consentedFirst = "consented before the widening"
		require.NoError(t, consentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, module), consentedFirst)
		deliverModule(t, ctx, conn, signer, widened)
		issued, err := issue(fixtureOrganization)
		require.NoError(t, err)
		for _, sc := range issued.GetContext().GetAuthorityScopes() {
			require.NotEqual(t, "modelservice.datasets", sc.GetResourceKind(), "the widening reached an installation that did not consent to it")
		}

		// The module's grant within the envelope drops the binding: what the
		// consent allowed no longer issues.
		registry := svc.ModulePrincipals()
		narrowed := svc.ModulePrincipals()
		grant := narrowed[business.ModulePrincipalID(module)]
		grant.OperationAudiences = nil
		narrowed[business.ModulePrincipalID(module)] = grant
		svc.SetModulePrincipals(narrowed)
		_, err = issue(fixtureOrganization)
		svc.SetModulePrincipals(registry)
		requireCode(t, err, codes.PermissionDenied, `operation binding "model" is not one its installation at `+fixtureOrganization+" consents to")
	})

	t.Run("a delegation is bounded by its grantor and ends with its revocation", func(t *testing.T) {
		client := authorityv1.NewAuthorityServiceClient(conn)
		alex, bob, alice := signIn("fixture-alex"), signIn("dev-bob"), signIn("dev-alice")
		_, err := client.Delegate(alex, &authorityv1.DelegateRequest{Module: "conformance-idle", Scopes: records("read"), Purpose: "uninstalled"})
		requireCode(t, err, codes.FailedPrecondition, `module "conformance-idle" is not installed in organization`)
		_, err = client.Delegate(bob, &authorityv1.DelegateRequest{Module: "org-directory", Scopes: records("write"), Purpose: "beyond"})
		requireCode(t, err, codes.PermissionDenied, "delegates what they do not hold")
		_, err = client.Delegate(alex, &authorityv1.DelegateRequest{Module: "org-directory", Scopes: records("read"), Purpose: strings.Repeat("p", authority.MaxDelegationPurpose+1)})
		requireCode(t, err, codes.InvalidArgument, "at most 1024 characters")

		d, err := client.Delegate(alex, &authorityv1.DelegateRequest{Module: "org-directory", Scopes: records("read"), Purpose: "a scheduled job"})
		require.NoError(t, err)
		require.True(t, d.GetActive())
		issue := func(scopes []*authorityv1.WorkScopeV1) (*authorityv1.IssueResponse, error) {
			return client.Issue(ctx, &authorityv1.IssueRequest{
				As:    &authorityv1.IssueRequest_Delegation{Delegation: &authorityv1.DelegationRef{DelegationId: d.GetDelegationId()}},
				Actor: &authorityv1.WorkloadCredential{Credential: workloadCredential}, OperationBindingId: "documents", Audience: "documents", Scopes: scopes,
			})
		}
		issued, err := issue(nil)
		require.NoError(t, err)
		require.Equal(t, d.GetGrantor(), issued.GetContext().GetOwnerPrincipalId())
		hops := issued.GetContext().GetActorChain()
		require.Len(t, hops, 1)
		require.Equal(t, d.GetDelegationId(), hops[0].GetDelegationId())
		require.Equal(t, []string{"read"}, hops[0].GetGrantedScopes()[0].GetActions())
		_, err = issue(records("invoke"))
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		_, err = client.Issue(ctx, &authorityv1.IssueRequest{
			As:    &authorityv1.IssueRequest_Delegation{Delegation: &authorityv1.DelegationRef{DelegationId: d.GetDelegationId()}},
			Actor: &authorityv1.WorkloadCredential{Credential: documentsCredential}, OperationBindingId: "model", Audience: "model",
		})
		requireCode(t, err, codes.PermissionDenied, "is granted to org-directory")
		_, err = client.RevokeDelegation(bob, &authorityv1.RevokeDelegationRequest{DelegationId: d.GetDelegationId()})
		requireCode(t, err, codes.NotFound, "delegation")

		own, err := client.Issue(alex, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}},
			Audience: "documents", Scopes: records("read")})
		require.NoError(t, err)
		revoked, err := client.RevokeDelegation(alex, &authorityv1.RevokeDelegationRequest{DelegationId: d.GetDelegationId()})
		require.NoError(t, err)
		require.False(t, revoked.GetDelegation().GetActive())
		again, err := client.RevokeDelegation(alex, &authorityv1.RevokeDelegationRequest{DelegationId: d.GetDelegationId()})
		require.NoError(t, err, "revoking twice answers the delegation as it stands")
		require.False(t, again.GetDelegation().GetActive())
		_, err = issue(nil)
		requireCode(t, err, codes.PermissionDenied, "is revoked")
		_, err = client.Check(ctx, &authorityv1.CheckRequest{Token: issued.GetToken(), Audience: "documents"})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "a capability issued before the revocation: %v", err)
		read, err := client.GetDelegation(ctx, &authorityv1.GetDelegationRequest{DelegationId: d.GetDelegationId(), Actor: &authorityv1.WorkloadCredential{Credential: workloadCredential}})
		require.NoError(t, err)
		require.False(t, read.GetActive())
		// The revocation moved the organization's revision, so the grantor's own
		// capability is issued again rather than relied on across it.
		_ = own

		bobs, err := client.Delegate(bob, &authorityv1.DelegateRequest{Module: "org-directory", Scopes: records("read"), Purpose: "a member's job"})
		require.NoError(t, err)
		ended, err := client.RevokeDelegation(alice, &authorityv1.RevokeDelegationRequest{DelegationId: bobs.GetDelegationId()})
		require.NoError(t, err, "an administrator of the organization ends a member's delegation")
		require.False(t, ended.GetDelegation().GetActive())
	})

	// Beyond the suite: every issue from one delegation is held to its own
	// scope set, whichever order the issues come in.
	t.Run("every issue from one delegation is held to its own scope set", func(t *testing.T) {
		client := authorityv1.NewAuthorityServiceClient(conn)
		alex := signIn("fixture-alex")
		for _, order := range [][]string{{"invoke", "lookup"}, {"lookup", "invoke"}} {
			d, err := client.Delegate(alex, &authorityv1.DelegateRequest{Module: "org-directory", Purpose: "two issues, " + strings.Join(order, " then "),
				Scopes: records("invoke", "read")})
			require.NoError(t, err)
			for _, operation := range order {
				issued, err := client.Issue(ctx, &authorityv1.IssueRequest{
					As:    &authorityv1.IssueRequest_Delegation{Delegation: &authorityv1.DelegationRef{DelegationId: d.GetDelegationId()}},
					Actor: &authorityv1.WorkloadCredential{Credential: workloadCredential}, OperationBindingId: "documents", Operation: operation, Audience: "documents",
				})
				require.NoError(t, err, "%v: issue under %s", order, operation)
				granted := issued.GetContext().GetActorChain()[0].GetGrantedScopes()
				want := map[string][]string{"invoke": {"invoke", "read"}, "lookup": {"read"}}[operation]
				require.Equal(t, want, granted[0].GetActions(), "%v: the %s issue carries its own scope set", order, operation)
				_, err = client.Check(ctx, &authorityv1.CheckRequest{Token: issued.GetToken(), Audience: "documents"})
				require.NoError(t, err, "%v: the %s issue checks", order, operation)
			}
		}
	})

	// Beyond the suite: the session a page reads is the live one. A fixture
	// person of the authority's fixture is current with their organisation, role
	// and team, the bearer is not handed back, and once the session's access is
	// revoked (a sign-out) the same bearer is no longer a session.
	t.Run("an ended session is no longer current", func(t *testing.T) {
		client := sessionv1.NewSessionServiceClient(conn)
		sess, err := client.SignIn(ctx, &sessionv1.SignInRequest{FixtureToken: "fixture-maya"})
		require.NoError(t, err)
		bearer := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+sess.GetBearer())
		current, err := client.Current(bearer, &sessionv1.CurrentRequest{})
		require.NoError(t, err)
		require.Equal(t, sess.GetSessionId(), current.GetSessionId())
		require.Equal(t, "00000000-0000-7000-8000-0000000000b1", current.GetPersonId())
		require.Equal(t, fixtureOrganization, current.GetOrganizationId())
		require.Equal(t, []string{"member"}, current.GetRoles())
		require.Len(t, current.GetTeamIds(), 1)
		require.Empty(t, current.GetBearer())

		require.NoError(t, minter.RevokeSessionAccess(ctx, sess.GetSessionId()))
		_, err = client.Current(bearer, &sessionv1.CurrentRequest{})
		require.Equal(t, codes.Unauthenticated, status.Code(err), "an ended session: %v", err)
	})

	// Beyond the suite: a fresh reviewed approval supersedes an unfinished
	// attempt; the superseded operation can neither resume nor acknowledge.
	t.Run("fresh approval supersedes pending attempt", func(t *testing.T) {
		const target = "conformance-pending-approval"
		page := wireScope(target+".page", "read")
		deliverSolution(t, ctx, conn, signer, conformanceSolution{alias: target, generation: 1, ceilings: []*authorityv1.WorkScopeV1{page},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {page}, "viewer": {page}}})
		admin := signIn("fixture-alex")
		client := installationv1.NewInstallationServiceClient(conn)
		review := func() *installationv1.ConsentApproval {
			a, err := client.Review(admin, &installationv1.ReviewRequest{OrganizationId: fixtureOrganization, Target: target})
			require.NoError(t, err)
			a.OperationId = uuid.NewString()
			return a
		}
		release := make(chan struct{})
		started := make(chan struct{}, 4)
		svc.SetAuthorityInstallStep(func(ctx context.Context, _ domain.Installation) error {
			started <- struct{}{}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		t.Cleanup(func() { svc.SetAuthorityInstallStep(nil) })
		install := func(approval *installationv1.ConsentApproval) chan error {
			done := make(chan error, 1)
			go func() {
				_, err := client.Install(admin, &installationv1.InstallRequest{OrganizationId: fixtureOrganization, Target: target, Role: "viewer", Approval: approval})
				done <- err
			}()
			return done
		}
		first := review()
		firstDone := install(first)
		<-started
		fresh := review()
		require.Greater(t, fresh.GetExpectedRevision(), uint64(0), "the review names the pending attempt's revision")
		freshDone := install(fresh)
		<-started
		close(release)
		require.NoError(t, <-freshDone, "the fresh approval acknowledges")
		err := <-firstDone
		requireCode(t, err, codes.FailedPrecondition, "INSTALLATION_APPROVAL")
		_, err = client.Install(admin, &installationv1.InstallRequest{OrganizationId: fixtureOrganization, Target: target, Role: "viewer", Approval: first})
		requireCode(t, err, codes.FailedPrecondition, "INSTALLATION_APPROVAL_REVISION_CHANGED")
		replayed, err := client.Install(admin, &installationv1.InstallRequest{OrganizationId: fixtureOrganization, Target: target, Role: "viewer", Approval: fresh})
		require.NoError(t, err, "the unchanged fresh request replays")
		require.Equal(t, installationv1.Installation_STATUS_ACTIVE, replayed.GetStatus())
	})

	// Beyond the suite: what the core changes is in the audit record, in the
	// transaction that changed it, naming the organization.
	// A capability is minted at the organization's authorization revision and
	// refused once it moves, so the host must move it only for a real change.
	// Idle reconcile passes, and fixture convergence retrying an installation
	// that cannot converge yet (a solution whose install step is not
	// acknowledged, so its teams wait), leave it where it is; a real change of
	// a role moves it.
	t.Run("idle reconcile passes and fixture convergence leave the authorization revision alone", func(t *testing.T) {
		revision := func() int64 {
			t.Helper()
			var r int64
			require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
				return storetx.Tx(ctx).QueryRow(ctx, `SELECT revision FROM public.organization_authorization_revisions WHERE org_id = $1`, fixtureOrganization).Scan(&r)
			}))
			return r
		}
		_ = installations.Converge(ctx) // the product's own records of the declared installations: a real change
		settled := revision()
		for range 3 {
			_ = reconciler.RunOnce(ctx)
			_ = installations.Converge(ctx)
		}
		require.Equal(t, settled, revision(), "an idle pass moved the authorization revision, superseding every capability minted before it")
		require.NoError(t, store.WithOrgTx(ctx, fixtureOrganization, func(ctx context.Context) error {
			return store.CreateRole(ctx, &gen.Role{Id: business.NewIDString(), OrgId: fixtureOrganization, Name: "Revision probe", Permissions: []*gen.Permission{{Resource: "example-records", Action: "read"}}})
		}))
		require.Greater(t, revision(), settled, "a real change of a role moves it")
	})

	t.Run("every change of authority is audited", func(t *testing.T) {
		var count int
		require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
			return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM public.audit_events WHERE event_type LIKE 'saas.authority.%' AND payload->>'organization' = $1`, fixtureOrganization).Scan(&count)
		}))
		require.Greater(t, count, 0)
		require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
			return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM public.audit_events WHERE event_type = 'saas.authority.role_revoked'`).Scan(&count)
		}))
		require.Greater(t, count, 0, "the suite's revocations are audited")
	})

	// A solution the composition delivers once the host is running is composed
	// in the authority core by the pass that applies it, before the fixture's
	// installations converge in that same pass: the declared installation is
	// then the core's too, active at the declaring organization alone, and the
	// declared team holds its role there.
	t.Run("a solution delivered to a running host is installed where the fixture declares it", func(t *testing.T) {
		reconciler.AfterEachPass(installations.Converge)
		t.Cleanup(func() { reconciler.AfterEachPass(nil) })
		note := wireScope("conformance-late.note", "read")
		deliverSolution(t, ctx, conn, signer, conformanceSolution{alias: "conformance-late", generation: 1, ceilings: []*authorityv1.WorkScopeV1{note},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {note}, "viewer": {note}}})
		installed := func(caller authority.Person, org string) (domain.Installation, bool) {
			t.Helper()
			all, err := core.Installations(ctx, caller, org)
			require.NoError(t, err)
			for _, inst := range all {
				if inst.Target == "conformance-late" {
					return inst, true
				}
			}
			return domain.Installation{}, false
		}
		inst, ok := installed(alex, fixtureOrganization)
		require.True(t, ok, "the pass that applied the delivery installed it in the core")
		require.Equal(t, domain.Active, inst.Status)
		_, ok = installed(authority.Person{ID: conformanceZoe, Organization: globexOrganization, SessionID: "conformance-harness"}, globexOrganization)
		require.False(t, ok, "and nowhere the fixture does not declare it")
		grants, err := core.ListRoleGrants(ctx, alex, fixtureOrganization, inst.ID)
		require.NoError(t, err)
		finance := teamNamed(t, ctx, svc, fixtureOrganization, "Finance")
		require.Contains(t, grants, domain.RoleGrant{InstallationID: inst.ID, Role: "viewer", TeamID: finance}, "the declared team holds its role")
	})

	// The artifact interface the local authority serves (host.artifact.v1),
	// asked as a module asks it: the module's workload credential and a parent
	// capability addressed to the module. accounts' one artifact decision
	// answers: an organization administrator present approves an exact
	// artifact under the module's installed policy; a member without the
	// policy's permission, a policy the module has none of, and consent through
	// another module's credential are each refused by name.
	t.Run("the artifact interface approves for an allowed caller and refuses otherwise", func(t *testing.T) {
		const module = "conformance-artifacts"
		const kind = "conformance-artifacts.intent"
		deliverModule(t, ctx, conn, signer, conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{wireScope(kind, "activate", "run")}})
		digest := "sha256:" + strings.Repeat("ab", 32)
		registry := svc.ModulePrincipals()
		t.Cleanup(func() { svc.SetModulePrincipals(registry) })
		withPolicy := svc.ModulePrincipals()
		grant := withPolicy[business.ModulePrincipalID(module)]
		grant.ArtifactPolicies = map[string]business.ExecutableArtifactPolicy{"intent-version": {
			Schema: "example.intent-version/v1", Sources: map[string]string{module: module},
			Activate: business.ArtifactPermission{Resource: kind, Action: "activate"}, Run: business.ArtifactPermission{Resource: kind, Action: "run"},
			Contracts: []business.ArtifactContract{{Kind: "engine", Name: "example-engine", Digest: digest}}, RequiredKinds: []string{"engine"},
		}}
		withPolicy[business.ModulePrincipalID(module)] = grant
		svc.SetModulePrincipals(withPolicy)
		require.NoError(t, consentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, module))
		installed, err := core.Installations(ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		var installation string
		for _, inst := range installed {
			if inst.Target == module {
				installation = inst.ID
			}
		}
		require.NotEmpty(t, installation)

		parentOf := func(token string, actions ...string) string {
			t.Helper()
			issued, err := authorityv1.NewAuthorityServiceClient(conn).Issue(signIn(token), &authorityv1.IssueRequest{
				As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: module,
				Scopes: []*authorityv1.WorkScopeV1{wireScope(kind, actions...)}})
			require.NoError(t, err, "%s at %s", token, module)
			return issued.GetToken()
		}
		approve := func(parent, actor, policy string) (*artifactv1.Approval, error) {
			return artifactv1.NewArtifactServiceClient(conn).Approve(ctx, &artifactv1.ArtifactRequest{
				ParentToken: parent, ActorCredential: actor, InstallationId: installation, Policy: policy,
				Identity: &artifactv1.Identity{Schema: "example.intent-version/v1", Source: module, Subject: []byte(`{"intent":"tool-assistant"}`),
					Contracts: []*artifactv1.Contract{{Kind: "engine", Name: "example-engine", Digest: digest}}},
			})
		}
		admin := parentOf("fixture-alex", "activate", "run")
		approved, err := approve(admin, credential(module), "intent-version")
		require.NoError(t, err, "an administrator present approves the artifact")
		require.True(t, strings.HasPrefix(approved.GetQualifiedName(), "host/approved-artifacts/"))
		authorized, err := artifactv1.NewArtifactServiceClient(conn).Authorize(ctx, &artifactv1.ArtifactRequest{
			ParentToken: admin, ActorCredential: credential(module), InstallationId: installation, Policy: "intent-version",
			Identity: &artifactv1.Identity{Schema: "example.intent-version/v1", Source: module, Subject: []byte(`{"intent":"tool-assistant"}`),
				Contracts: []*artifactv1.Contract{{Kind: "engine", Name: "example-engine", Digest: digest}}},
		})
		require.NoError(t, err, "a run is authorized against the standing approval")
		require.Equal(t, approved.GetQualifiedName(), authorized.GetQualifiedName())

		_, err = approve(admin, credential(module), "no-such-policy")
		requireCode(t, err, codes.PermissionDenied, "installed artifact policy required")
		_, err = approve(admin, credential("documents"), "intent-version")
		require.Error(t, err, "a parent addressed to the module is refused to another module's credential")
		_, err = approve(admin, "", "intent-version")
		requireCode(t, err, codes.PermissionDenied, "")
		_, err = approve(parentOf("fixture-alex", "run"), credential(module), "intent-version")
		requireCode(t, err, codes.PermissionDenied, "does not hold "+kind+":activate")
		_, err = authorityv1.NewAuthorityServiceClient(conn).Issue(signIn("dev-bob"), &authorityv1.IssueRequest{
			As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: module,
			Scopes: []*authorityv1.WorkScopeV1{wireScope(kind, "activate")}})
		requireCode(t, err, codes.PermissionDenied, "beyond what the owner holds")
	})

	// Last, because the envelope it writes becomes every module's ceiling: a
	// real envelope write that narrows a module's binding (the write
	// PutModuleEnvelope makes, without its platform administrator check) is
	// refused at once until the registry is derived again, then issues only
	// what the narrowed ceiling allows, and a capability issued before it
	// fails its next check.
	t.Run("an envelope narrowing a binding ends what was issued through it", func(t *testing.T) {
		client := authorityv1.NewAuthorityServiceClient(conn)
		const module = "conformance-envelope"
		deliverModule(t, ctx, conn, signer, conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-envelope.task", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"headless": {"modelservice.profiles:read", "modelservice.datasets:read"}}}}})
		workloadOf := credential(module)
		issue := func() (*authorityv1.IssueResponse, error) {
			return client.Issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Workload{Workload: &authorityv1.WorkloadCredential{Credential: workloadOf}},
				OperationBindingId: "model", Audience: "model", OrganizationId: fixtureOrganization})
		}
		kinds := func(r *authorityv1.IssueResponse) []string {
			var out []string
			for _, sc := range r.GetContext().GetAuthorityScopes() {
				out = append(out, sc.GetResourceKind())
			}
			return out
		}
		require.NoError(t, consentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, module))
		before, err := issue()
		require.NoError(t, err)
		require.Contains(t, kinds(before), "modelservice.datasets")

		var latest uint64
		require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			latest, err = store.LatestModuleEnvelopeRevision(ctx)
			return err
		}))
		doc := []byte(fmt.Sprintf(`{"revision":%d,"principals":{%q:{"grant":{"tenant":%q,"resources":["conformance-envelope.task"],"operation_audiences":{"model":{"audience":"model","headless_scopes":[{"resource_kind":"modelservice.profiles","actions":["read"]}]}}}}}}`,
			latest+1, module, fixtureOrganization))
		_, err = business.ParseModuleEnvelope(doc)
		require.NoError(t, err)
		require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
			return store.AppendModuleEnvelopeRecord(ctx, &business.ModuleEnvelopeRecord{Revision: latest + 1, Document: doc, WrittenBy: conformanceAlex}, latest)
		}))

		_, err = client.Check(ctx, &authorityv1.CheckRequest{Token: before.GetToken(), Audience: "model"})
		require.Error(t, err, "a capability issued under the replaced ceiling fails its next check")
		_, err = issue()
		requireCode(t, err, codes.Unavailable, "is committed")

		pass, err := business.NewSolutionHostBindingReconciler(svc, business.SolutionHostBindingReconcilerConfig{
			Source: business.NewDeliveredSolutionHostBindings(svc), Verifier: verifier, Coordinate: "conformance-host", Envelopes: svc,
		})
		require.NoError(t, err)
		// The envelope names only this module, so the pass reports every other
		// composed module as omitted from the registry; it still installs the
		// registry this module's narrowed grant is read from.
		if err := pass.RunOnce(ctx); err != nil {
			require.ErrorContains(t, err, "omitted from the registry")
			require.NotContains(t, err.Error(), module)
		}
		after, err := issue()
		require.NoError(t, err)
		require.Equal(t, []string{"modelservice.profiles"}, kinds(after), "the narrowed ceiling issues only what it allows")
		_, err = client.Check(ctx, &authorityv1.CheckRequest{Token: before.GetToken(), Audience: "model"})
		require.Error(t, err, "and the capability issued before stays refused")
	})
}

// serveConnect holds its :0 listener until cleanup and reads the assigned port
// back from it. Reuse the generated registration and production interceptors.
func serveConnect(t *testing.T, ctx context.Context, grpcServer *GrpcServer) uint16 {
	t.Helper()
	tier, err := NewConnectServer(&Configuration{}, grpcServer)
	require.NoError(t, err)
	mux := http.NewServeMux()
	otel, err := otelconnect.NewInterceptor()
	require.NoError(t, err)
	registerCatalogConnectServices(mux, tier, connect.WithInterceptors(otel,
		connectRefusalInterceptor(), connectAuthInterceptor(func() auth.JWTMinter {
			return service.JWTMinter()
		}), rateLimitInterceptor(rateLimiter)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &http.Server{Handler: h2c.NewHandler(mux, &http2.Server{}), ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	ran := make(chan error, 1)
	go func() { ran <- server.Serve(listener) }()
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.ErrorIs(t, <-ran, http.ErrServerClosed)
	})
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

// registeredDescriptors is the descriptor set of every proto file this binary
// links, which is what codefly generate contracts would export for the served
// interfaces; the suite drives the interfaces from it with dynamic messages.
func registeredDescriptors(t *testing.T) []byte {
	t.Helper()
	set := &descriptorpb.FileDescriptorSet{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
		return true
	})
	raw, err := proto.Marshal(set)
	require.NoError(t, err)
	return raw
}

// memoryRevoker is an in-memory access revocation list behind auth.TokenRevoker.
type memoryRevoker struct{ tokens, sessions sync.Map }

func (r *memoryRevoker) Revoke(_ context.Context, jti string, _ time.Duration) error {
	r.tokens.Store(jti, true)
	return nil
}
func (r *memoryRevoker) IsRevoked(_ context.Context, jti string) (bool, error) {
	_, revoked := r.tokens.Load(jti)
	return revoked, nil
}
func (r *memoryRevoker) RevokeSession(_ context.Context, sessionID string, _ time.Duration) error {
	r.sessions.Store(sessionID, true)
	return nil
}
func (r *memoryRevoker) IsSessionRevoked(_ context.Context, sessionID string) (bool, error) {
	_, revoked := r.sessions.Load(sessionID)
	return revoked, nil
}

// These test owners stand in for the platform's delivery account and approval.
type conformanceCarrier struct{}

func (conformanceCarrier) AuthorizeCarrier(_ context.Context, credential, namespace string) error {
	if credential != "Bearer delivery:"+namespace {
		return fmt.Errorf("not the delivery workload of %s", namespace)
	}
	return nil
}
