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
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	conformance "github.com/codefly-dev/authority/libraries/conformance/go"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	modulecontract "github.com/codefly-dev/core/contracts/module"
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
	conformanceIvy      = "00000000-0000-7000-8000-0000000000b9"
	conformanceZoe      = "00000000-0000-7000-8000-0000000000b5"
	conformanceMaya     = "00000000-0000-7000-8000-0000000000b1"
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
	faults := &installFaults{PostgresStore: store}
	svc, err := business.NewService(faults)
	require.NoError(t, err)
	// The row-backed Work Context seal reads, as work.go wires them: the host
	// resolves a module page's installation from the core's record through it.
	svc.SetWorkContextSealStore(store)
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
	// The suite's reach case delivers conformance-reach and acts as its
	// workload with the credential it delivered it with: that one delivery
	// credential is the workload's own, and the carrier accepts it for it.
	deliveryWorkloads := map[string]string{"conformance-reach": credential("conformance-reach")}
	deliveryCredential := func(module string) string {
		if workload, ok := deliveryWorkloads[module]; ok {
			return workload
		}
		return "delivery:" + module
	}
	svc.SetSolutionDelivery(verifier, store, conformanceCarrier{workloads: deliveryWorkloads})
	svc.SetSolutionDeliveryCoordinate("conformance-host")
	svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
	})
	svc.SetModuleEnvelopeStore(store)
	svc.SetAdmissionKeyStore(store)
	pagePayload, pageSource := conformancePage(t)
	svc.SetFrontendArtifactSource(pageSource)
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
	acknowledgeSetup := canonicalConformanceStep(t, ctx, svc, store)
	svc.SetAuthorityInstallStep(acknowledgeSetup)

	// The Connect listener is the catalog-complete one and speaks the gRPC
	// protocol over h2c, so the suite dials it with a plain gRPC client.
	port := serveConnect(t, ctx, grpcServer)
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// The composition, delivered as the CLI delivers it. records is the
	// audience the viewer reads at; it checks no owner, so a person's
	// organization roles reach it on their own session. documents checks
	// owners: an organization role reaches nothing there on a person's own
	// session (RBAC-4a), and a solution's role or an installation reaches its
	// own principal's data. org-directory is the page module reading records
	// for the viewer through its one binding; model an audience the viewer's
	// context at records is not addressed to, checking owners of its datasets
	// with its profiles and templates shared. records and model bind to each
	// other, so a delegate chain crosses from one to the other and back; model
	// also binds to itself across its three kinds, and documents to the
	// model's profiles and datasets. conformance-agent is a module whose tools binding reaches
	// an installed solution; conformance-idle a declared workload with no
	// active installation.
	modules := []conformanceModule{
		{alias: "records", ownerUnaware: true, ceilings: []*authorityv1.WorkScopeV1{wireScope("example-entries", "invoke", "read", "write")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.profiles:invoke", "modelservice.profiles:read"}}}}},
		{alias: "documents", ceilings: []*authorityv1.WorkScopeV1{wireScope("example-records", "invoke", "read", "write", "ingest")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.profiles:invoke", "modelservice.profiles:read", "modelservice.datasets:read"}}}}},
		{alias: "org-directory", ownerUnaware: true, ceilings: []*authorityv1.WorkScopeV1{wireScope("directory.entry", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "records", Audience: "records", Operations: map[string][]string{
				"invoke": {"example-entries:invoke", "example-entries:read"}, "lookup": {"example-entries:read"}, "headless": {"example-entries:invoke", "example-entries:read"}}}}},
		{alias: "model", sharedKinds: []string{"modelservice.profiles", "modelservice.templates"}, ceilings: []*authorityv1.WorkScopeV1{wireScope("modelservice.profiles", "invoke", "read"), wireScope("modelservice.datasets", "read"), wireScope("modelservice.templates", "read")},
			bindings: []solutionhost.AuthorityBinding{
				{ID: "records", Audience: "records", Operations: map[string][]string{"invoke": {"example-entries:invoke", "example-entries:read"}, "lookup": {"example-entries:read"}}},
				{ID: "self", Audience: "model", Operations: map[string][]string{
					"invoke":   {"modelservice.profiles:invoke", "modelservice.profiles:read", "modelservice.datasets:read", "modelservice.templates:read"},
					"headless": {"modelservice.profiles:invoke", "modelservice.profiles:read", "modelservice.datasets:read", "modelservice.templates:read"}}},
			}},
		// conformance-agent's tools binding carries the person's role on the
		// proposals solution to that solution; its conversations are shared,
		// so both people use the agent on their own session through their
		// organization roles.
		{alias: "conformance-agent", sharedKinds: []string{"conformance-agent.conversation"}, ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-agent.conversation", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "tools", Audience: "conformance-proposals", Operations: map[string][]string{"invoke": {"conformance-proposals.item:read"}}}}},
		{alias: "conformance-idle", ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-idle.task", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.profiles:read"}, "headless": {"modelservice.profiles:read"}}}}},
	}
	// conformance-idle stays a declared workload installed nowhere: the
	// harness holds its composition back.
	faults.blockComposition("conformance-idle")
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

	// The artifact target's module: one kind, with the two actions an artifact
	// decision asks of a policy. Installed at Acme alone, so Globex's
	// administrator lists installations the module can never name.
	const artifactModule = "conformance-artifact"
	faults.blockComposition(artifactModule)
	// It checks no owner: the artifact decision is reached through the
	// people's organization roles on their own session, which reach no owned
	// kind of a module that checks owners (RBAC-4a).
	deliverModule(t, ctx, conn, signer, conformanceModule{alias: artifactModule, ownerUnaware: true,
		ceilings: []*authorityv1.WorkScopeV1{wireScope(artifactModule+".intent", "activate", "run"), wireScope(artifactModule+".preview", "activate", "run")}})
	require.NoError(t, consentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, artifactModule))

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
		// An installation that acts for itself down a chain of the modules
		// that check owners: documents, then the model's datasets.
		alias: "conformance-assistant", generation: 1,
		ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-assistant.page", "read")},
		roles: map[string][]*authorityv1.WorkScopeV1{
			"installer": {wireScope("conformance-assistant.page", "read")},
			"viewer": {wireScope("conformance-assistant.page", "read"), wireScope("example-records", "read"),
				wireScope("modelservice.profiles", "invoke", "read"), wireScope("modelservice.datasets", "read")},
		},
		bindings: []solutionhost.AuthorityBinding{
			{ID: "documents", Audience: "documents", Operations: map[string][]string{"invoke": {"example-records:read"}}},
			{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.profiles:invoke", "modelservice.profiles:read", "modelservice.datasets:read"}}},
		},
	}} {
		deliverSolution(t, ctx, conn, signer, solution)
	}
	// Two installed solutions for installation-owned role scopes: Alex installs
	// both, and Maya's finance team holds A's viewer role and nothing of B's.
	// Each is consented to at the surface the suite delivers again at its own
	// generation (the store it binds, its roles and its owned scopes), so that
	// redelivery asks nothing new and the consent re-converges on it: a
	// holder holds what the consent approved, never a wider delivered offer.
	const collection = "conformance.collection"
	deliverModule(t, ctx, conn, signer, conformanceModule{alias: "conformance-store", ceilings: []*authorityv1.WorkScopeV1{wireScope(collection, "admin", "create", "read", "write")},
		bindings: []solutionhost.AuthorityBinding{{ID: "self", Audience: "conformance-store", Operations: map[string][]string{
			"invoke": {collection + ":create", collection + ":read"}, "headless": {collection + ":create", collection + ":read"}}}}})
	require.NoError(t, installModule(ctx, "conformance-store"))
	for _, owned := range []string{"conformance-owner-a", "conformance-owner-b"} {
		note := wireScope(owned+".note", "read")
		deliverSolution(t, ctx, conn, signer, conformanceSolution{alias: owned, generation: 1, ceilings: []*authorityv1.WorkScopeV1{note},
			roles: map[string][]*authorityv1.WorkScopeV1{
				"installer": {note, wireScope(collection, "create"), wireScope(collection, "read"), wireScope(collection, "admin"), wireScope(collection, "write")},
				"viewer":    {note}},
			owned: map[string][]*authorityv1.WorkScopeV1{"viewer": {wireScope(collection, "read")}},
			bindings: []solutionhost.AuthorityBinding{{ID: "store", Audience: "conformance-store", Operations: map[string][]string{
				"invoke": {collection + ":create", collection + ":read", collection + ":admin", collection + ":write"}}}}})
	}
	require.NoError(t, installations.ConvergeAuthority(ctx), "every declared installation converges once its target is delivered")
	installed := func(target string) string { return authority.StableInstallationID(fixtureOrganization, target) }
	proposals, assistant := installed("conformance-proposals"), installed("conformance-assistant")
	finance := teamNamed(t, ctx, svc, fixtureOrganization, "Finance")
	// Alex explicitly installs for Ivy, a member and no administrator, who
	// holds the separately declared installer role; Maya's finance team holds
	// A's viewer role and nothing of B's.
	for _, owned := range []string{"conformance-owner-a", "conformance-owner-b"} {
		_, err := installConformanceSolution(ctx, svc, core, owned, "viewer", conformanceIvy)
		require.NoError(t, err)
		_, err = core.GrantRoleAsHost(ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: installed(owned), Role: domain.InstallerRole, PersonID: conformanceIvy})
		require.NoError(t, err)
	}
	_, err = core.GrantRoleAsHost(ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: installed("conformance-owner-a"), Role: "viewer", TeamID: finance})
	require.NoError(t, err)

	documentsCredential, recordsCredential, modelCredential, workloadCredential := credential("documents"), credential("records"), credential("model"), credential("org-directory")
	// A delegate chain: an administrator issued at records on his own session;
	// records hops to model through its binding for what the first token never
	// carried (the model's shared profiles), then model hops back to records
	// through its own.
	chain := conformance.Chain{Token: "fixture-alex", Start: "records", Hops: []conformance.Hop{
		{Credential: recordsCredential, Binding: "model", Audience: "model", AskKind: "modelservice.profiles", AskAction: "invoke"},
		{Credential: modelCredential, Binding: "records", Audience: "records", AskKind: "example-entries", AskAction: "read"},
	}}
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization, SessionID: "conformance-harness"}
	const adminToken = "dev-alice"
	administrator, err := sessionv1.NewSessionServiceClient(conn).SignIn(ctx, &sessionv1.SignInRequest{FixtureToken: adminToken})
	require.NoError(t, err)
	conformance.Run(t, conn, registeredDescriptors(t), conformance.Fixture{
		Organization:   fixtureOrganization,
		HostCoordinate: "conformance-host", HostDomain: "acme",
		SignManifest:       func(doc []byte) ([]byte, error) { return solutionhost.SignEnvelope(doc, signer) },
		SignAuthority:      func(doc []byte) ([]byte, error) { return delivery.Sign(doc, signer.KeyID(), signer.Sign) },
		DeliveryCredential: deliveryCredential,
		InstallModule:      installModule,
		PagePayload:        pagePayload,
		// accounts serves OfferService from the same core, so the declaration
		// case compares the reviewed offer here too: a solution's roles and the
		// permissions they carry are its declaration on either host.
		ViewerToken: "dev-bob", AdminToken: adminToken,
		Audience: "records", ReadKind: "example-entries",
		WorkloadModule: "org-directory", WorkloadCredential: workloadCredential, Binding: "records", BindingAudience: "records", Elsewhere: "model",
		// The installed proposals solution and its own workload. Its roles name
		// its own kind alone, so records (Audience) is unrelated to it.
		InstalledSolution: proposals, InstalledAudience: "conformance-proposals", InstalledCredential: credential("conformance-proposals"),
		// An Acme owner who is not an administrator; a member with no role and no team.
		OwnerToken: "fixture-owner", UnentitledToken: "fixture-no-roles",
		// Globex's administrator, and the hop's own workload credential (service
		// account hop in the authority module's namespace).
		OtherAdminToken: "fixture-zoe",
		HopCredential:   cluster.Run(executiontest.Workload{Namespace: business.HopModule, ServiceAccount: business.HopService, Container: "app"}),
		EdgeCredential:  cluster.Run(executiontest.Workload{Namespace: business.HopModule, ServiceAccount: business.ModuleEdgeService, Container: "app"}),
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
			Ambiguous: conformance.WorkloadOrganizationCase{Credential: workloadCredential, Module: "org-directory", Binding: "records", Audience: "records"},
			None:      conformance.WorkloadOrganizationCase{Credential: credential("conformance-idle"), Module: "conformance-idle", Binding: "model", Audience: "model"},
		},
		// Maya reads one example entry by grant, not the kind.
		Resources: &conformance.ResourceCase{Token: "fixture-maya", Audience: "records", Kind: "example-entries", Action: "read",
			Covered: []string{"conformance-record-a"}, Uncovered: []string{"conformance-record-b"}, Unheld: "write"},
		// Alex lets the directory page invoke example entries for him; Bob, a
		// reader, cannot delegate what he does not hold.
		Delegation: &conformance.DelegationCase{Token: "fixture-alex", Outsider: "dev-bob", Module: "org-directory", Credential: workloadCredential,
			Binding: "records", Audience: "records", Kind: "example-entries", Action: "invoke"},
		// Alex's chain below, projected through each target binding.
		ChildScopes: &conformance.ChildScopesCase{Chain: chain, FirstActions: []string{"invoke", "read"}, NextActions: []string{"invoke", "read"},
			OutsideKind: "example-entries", OutsideAction: "read", ResourceID: "a", OtherResourceID: "b", DroppedAction: "invoke"},
		// Alex at model, out to records and back, keeps what the root narrowed.
		ParentLimits: &conformance.ParentLimitsCase{Token: "fixture-alex", Root: "model", Credential: modelCredential, SelfBinding: "self",
			Kind: "modelservice.profiles", DroppedKind: "modelservice.templates", Action: "read", DroppedAction: "invoke", ResourceID: "a", OtherResourceID: "b",
			Out:  conformance.Hop{Credential: modelCredential, Audience: "records", Binding: "records"},
			Back: conformance.Hop{Credential: recordsCredential, Audience: "model", Binding: "model"}},
		// The assistant's installation, acting for itself through the two
		// modules that check owners.
		InstallationChain: &conformance.InstallationChainCase{Installation: assistant, Source: "conformance-assistant", Credential: credential("conformance-assistant"),
			Hops: []conformance.Hop{
				{Credential: credential("conformance-assistant"), Binding: "documents", Audience: "documents", AskKind: "example-records", AskAction: "read"},
				{Credential: documentsCredential, Binding: "model", Audience: "model", AskKind: "modelservice.datasets", AskAction: "read"},
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
			PauseInstall: func() func() {
				release := make(chan struct{})
				svc.SetAuthorityInstallStep(func(ctx context.Context, inst domain.Installation) error {
					select {
					case <-release:
						return acknowledgeSetup(ctx, inst)
					case <-ctx.Done():
						return ctx.Err()
					}
				})
				var once sync.Once
				return func() { once.Do(func() { close(release); svc.SetAuthorityInstallStep(acknowledgeSetup) }) }
			},
			Revoke: func(ctx context.Context, id string) error { return core.Uninstall(ctx, alex, fixtureOrganization, id) },
			SetAdministrator: func(ctx context.Context, enabled bool) error {
				role := gen.OrgRole_ORG_ROLE_MEMBER
				if enabled {
					role = gen.OrgRole_ORG_ROLE_ADMIN
				}
				// The normal membership upsert changes live standing without
				// ending the session, and invalidates the membership cache.
				return svc.AddOrgMember(ctx, conformanceAlex, &gen.AddOrgMemberRequest{
					OrgId: fixtureOrganization, UserId: administrator.GetPersonId(), Role: role,
				})
			},
		},
		Chain: &chain,
		// The same binding read as a lookup carries its lookup scopes alone.
		Lookup: &conformance.LookupCase{Token: "fixture-alex", Start: "model", Credential: modelCredential, Binding: "records",
			Audience: "records", Kind: "example-entries", Invoke: "invoke", Lookup: "read"},
		// The permission matrix: a record viewer, Acme administrators, a member
		// who holds no role there, and a Globex member at Acme's content. At
		// documents, which checks owners, no organization role reaches anything
		// on a person's own session, an administrator's every kind included
		// (RBAC-4a); at the model only its shared kinds.
		Matrix: []conformance.Expectation{
			{Token: "dev-bob", Audience: "records", Kind: "example-entries", Action: "read", Allowed: true},
			{Token: "dev-bob", Audience: "records", Kind: "example-entries", Action: "write", Allowed: false},
			{Token: "dev-alice", Audience: "records", Kind: "example-entries", Action: "write", Allowed: true},
			{Token: "fixture-alex", Audience: "model", Kind: "modelservice.profiles", Action: "invoke", Allowed: true},
			{Token: "fixture-alex", Audience: "model", Kind: "modelservice.datasets", Action: "read", Allowed: false},
			{Token: "fixture-maya", Audience: "model", Kind: "modelservice.profiles", Action: "invoke", Allowed: false},
			{Token: "fixture-omar", Audience: "records", Kind: "example-entries", Action: "read", Allowed: false},
			{Token: "dev-alice", Audience: "documents", Kind: "example-records", Action: "write", Allowed: false},
			{Token: "fixture-alex", Audience: "documents", Kind: "example-records", Action: "read", Allowed: false},
			{Token: "dev-bob", Audience: "documents", Kind: "example-records", Action: "read", Allowed: false},
		},
	})

	// The artifact target (conformance RunArtifacts): the one artifact decision
	// every host makes, the shared core's. Alex administers Acme, so he holds
	// every action on every kind; the approver holds the policy's activate and
	// run through a role and the runner its run alone, which is what proves
	// consent is the action rather than an administrator tier. Globex's
	// administrator is the outsider: the module is installed at Acme only (its
	// composition is held back, and Acme's administrator installs it).
	conformance.RunArtifacts(t, conn, registeredDescriptors(t), conformance.ArtifactCase{
		Module: artifactModule, Credential: credential(artifactModule), OtherCredential: credential("org-directory"),
		Policy: artifactModule + ".intent",
		Admin:  "fixture-alex", Approver: "fixture-artifact-approver", Member: "fixture-artifact-runner", OtherAdmin: "fixture-zoe",
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
		EmptyTeam: teamNamed(t, ctx, svc, fixtureOrganization, "Principal empty team"),
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
	t.Run("artifact consent stays with its policy", func(t *testing.T) {
		testHostArtifactPolicyIsolation(t, ctx, conn, signIn("fixture-alex"), artifactModule, credential(artifactModule))
	})
	requireCode := func(t *testing.T, err error, code codes.Code, reason string) {
		t.Helper()
		require.Equal(t, code, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), reason)
	}
	entries := func(actions ...string) []*authorityv1.WorkScopeV1 {
		return []*authorityv1.WorkScopeV1{{ResourceKind: "example-entries", Actions: actions}}
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
			As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: "records", Scopes: entries("read")})
		requireCode(t, err, codes.PermissionDenied, "not a member of this organization")
	})

	// Beyond the suite: OfferService answers from the core's view of the
	// delivered declaration. An administrator reviews an installed solution's
	// roles → operations → tools and its permission ceiling, with the consent
	// tuple; another organization's administrator is refused by name; anyone
	// else gets exactly what the core answers them, never a tuple.
	t.Run("an administrator reviews an installed solution's roles, operations and ceiling", func(t *testing.T) {
		offers := installationv1.NewOfferServiceClient(conn)
		const target = "conformance-pages"
		composed, err := core.Composed(ctx, target)
		require.NoError(t, err)

		admin := signIn("fixture-alex")
		listed, err := offers.ListOffers(admin, &installationv1.ListOffersRequest{OrganizationId: fixtureOrganization})
		require.NoError(t, err)
		var names []string
		for _, o := range listed.GetOffers() {
			names = append(names, o.GetTarget())
		}
		require.Contains(t, names, target)
		require.NotContains(t, names, "documents", "a module is installed by composition, never offered")

		review, err := offers.ReviewOffer(admin, &installationv1.ReviewRequest{OrganizationId: fixtureOrganization, Target: target})
		require.NoError(t, err)
		require.Equal(t, target, review.GetTarget())
		require.Equal(t, composed.Generation, review.GetApproval().GetGeneration())
		require.NotEmpty(t, review.GetApproval().GetDigest(), "an administrator reads the consent tuple")
		require.Equal(t, authority.StableInstallationID(fixtureOrganization, target), review.GetInstallation().GetId())
		require.Equal(t, installationv1.Installation_STATUS_ACTIVE, review.GetInstallation().GetStatus())
		require.False(t, review.GetToolsComposed(), "this host composes no tool bindings and says so")

		var roles []string
		reaches := map[string][]string{}
		for _, role := range review.GetRoles() {
			roles = append(roles, role.GetName())
			require.NotEmpty(t, role.GetScopes(), "role %s states its scopes", role.GetName())
			for _, c := range role.GetCapabilities() {
				reaches[role.GetName()] = append(reaches[role.GetName()], c.GetModule()+" "+c.GetOperation())
				require.NotEmpty(t, c.GetRequires())
				require.Empty(t, c.GetTools(), "no tool without composed bindings")
			}
		}
		require.ElementsMatch(t, slices.Collect(maps.Keys(composed.Roles)), roles, "every declared role, and only those")
		require.Contains(t, reaches["viewer"], target+" GET /items/{id}", "the viewer reaches the read it declares")
		require.NotContains(t, reaches["viewer"], target+" POST /items/{id}", "and never the write")
		require.NotContains(t, reaches["viewer"], target+" GET /open/{path=**}", "an operation any checked caller reaches needs no role")

		var ceiling []string
		for _, b := range review.GetCeiling() {
			ceiling = append(ceiling, b.GetBinding()+"→"+b.GetModule())
			require.NotEmpty(t, b.GetScopes())
		}
		require.Contains(t, ceiling, "documents→documents", "the ceiling is every binding the solution declares")
		require.Len(t, ceiling, len(composed.Bindings))

		// Globex's administrator learns nothing of Acme's offers.
		zoe := signIn("fixture-zoe")
		_, err = offers.ReviewOffer(zoe, &installationv1.ReviewRequest{OrganizationId: fixtureOrganization, Target: target})
		requireCode(t, err, codes.PermissionDenied, fixtureOrganization)
		_, err = offers.ListOffers(zoe, &installationv1.ListOffersRequest{OrganizationId: fixtureOrganization})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)

		// A member is answered exactly as the core answers them: its refusal
		// verbatim, or the offer without the consent tuple.
		member := signIn("fixture-maya")
		session, err := sessionv1.NewSessionServiceClient(conn).Current(member, &sessionv1.CurrentRequest{})
		require.NoError(t, err)
		_, coreErr := core.ReviewOffer(ctx, authority.Person{ID: session.GetPersonId(), Organization: session.GetOrganizationId(), SessionID: session.GetSessionId()}, fixtureOrganization, target)
		answer, err := offers.ReviewOffer(member, &installationv1.ReviewRequest{OrganizationId: fixtureOrganization, Target: target})
		if coreErr != nil {
			require.Equal(t, status.Code(coreStatus(coreErr)), status.Code(err), "%v", err)
			require.Equal(t, coreErr.Error(), status.Convert(err).Message())
		} else {
			require.NoError(t, err)
			require.Nil(t, answer.GetApproval(), "only an administrator reads the consent tuple")
			require.NotEmpty(t, answer.GetRoles())
		}
	})

	t.Run("reviewed roles remain from the review after redelivery", func(t *testing.T) {
		const target = "conformance-review-snapshot"
		scopes := []*authorityv1.WorkScopeV1{wireScope(target+".item", "read")}
		declared := conformanceSolution{alias: target, generation: 1, ceilings: scopes,
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": scopes, "viewer": scopes}}
		deliverSolution(t, ctx, conn, signer, declared)
		_, err := installConformanceSolution(ctx, svc, core, target, "viewer", conformanceAlex)
		require.NoError(t, err)
		reviewed, err := core.ReviewOffer(ctx, alex, fixtureOrganization, target)
		require.NoError(t, err)

		// Delivery changes after the consistent read, before its wire mapping.
		declared.generation = 2
		declared.roles = map[string][]*authorityv1.WorkScopeV1{"installer": scopes, "viewer": scopes, "later": scopes}
		deliverSolution(t, ctx, conn, signer, declared)
		current, err := core.Composed(ctx, target)
		require.NoError(t, err)
		require.Contains(t, current.Roles, "later")

		wire, err := reviewedOffer(reviewed, nil)
		require.NoError(t, err)
		require.Equal(t, uint64(1), wire.GetApproval().GetGeneration())
		require.Equal(t, []string{"installer", "viewer"}, wire.GetInstallation().GetRoles())
		var roles []string
		for _, role := range wire.GetRoles() {
			roles = append(roles, role.GetName())
		}
		require.Equal(t, wire.GetInstallation().GetRoles(), roles)
	})

	// Beyond the suite: a delegation never reaches past what its grantor holds
	// or what it names; once revoked, by its grantor or an administrator of its
	// organization, it issues nothing and every capability issued from it fails
	// its next check. Anyone else learns nothing of it.
	// A module is installed at every organization by composition, at its
	// delivery: its workload is issued at each, nothing past the organization
	// the module envelope pins it to, and nothing once its withdrawal is
	// delivered, when what it was issued fails its next check.
	t.Run("a module workload is issued only where it is installed, within its pin, until withdrawn", func(t *testing.T) {
		client := authorityv1.NewAuthorityServiceClient(conn)
		const module = "conformance-consent"
		deliverModule(t, ctx, conn, signer, conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-consent.task", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"headless": {"modelservice.profiles:read"}}}}})
		workloadOf := credential(module)
		issue := func(org string) (*authorityv1.IssueResponse, error) {
			return client.Issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Workload{Workload: &authorityv1.WorkloadCredential{Credential: workloadOf}},
				OperationBindingId: "model", Audience: "model", OrganizationId: org})
		}
		issued, err := issue(fixtureOrganization)
		require.NoError(t, err, "installed at Acme by composition, at its delivery")
		require.Equal(t, fixtureOrganization, issued.GetContext().GetTenantId())
		_, err = issue(globexOrganization)
		require.NoError(t, err, "installed at Globex by composition too")

		// Pinned to Acme by the module envelope, the workload is issued nothing
		// at Globex though it is installed there.
		registry := svc.ModulePrincipals()
		pinned := registry[business.ModulePrincipalID(module)]
		pinned.Tenant = fixtureOrganization
		pinnedRegistry := svc.ModulePrincipals()
		pinnedRegistry[business.ModulePrincipalID(module)] = pinned
		svc.SetModulePrincipals(pinnedRegistry)
		_, err = issue(globexOrganization)
		svc.SetModulePrincipals(registry)
		requireCode(t, err, codes.PermissionDenied, "is bound to organization "+fixtureOrganization)

		// Nobody uninstalls a module; its delivered withdrawal retires it:
		// nothing more is issued, and what was issued fails its next check.
		installed, err := core.Installations(ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		var id string
		for _, inst := range installed {
			if inst.Target == module {
				id = inst.ID
			}
		}
		require.NotEmpty(t, id)
		require.Error(t, svc.UninstallSolution(ctx, conformanceAlex, fixtureOrganization, id), "a person never uninstalls a module")
		withdrawModule(t, ctx, conn, signer, module, 2)
		_, err = issue(fixtureOrganization)
		require.Error(t, err, "a withdrawn module's workload is issued nothing")
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

		// Acme narrows its installation, made by composition, to no capability.
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
		_, err = issue(globexOrganization)
		require.NoError(t, err, "installed at Globex by composition")
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
		require.NoError(t, reconsentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, module), consentedFirst)
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
		_, err := client.Delegate(alex, &authorityv1.DelegateRequest{Module: "conformance-idle", Scopes: entries("read"), Purpose: "uninstalled"})
		requireCode(t, err, codes.FailedPrecondition, `module "conformance-idle" is not installed in organization`)
		_, err = client.Delegate(bob, &authorityv1.DelegateRequest{Module: "org-directory", Scopes: entries("write"), Purpose: "beyond"})
		requireCode(t, err, codes.PermissionDenied, "delegates what they do not hold")
		_, err = client.Delegate(alex, &authorityv1.DelegateRequest{Module: "org-directory", Scopes: entries("read"), Purpose: strings.Repeat("p", authority.MaxDelegationPurpose+1)})
		requireCode(t, err, codes.InvalidArgument, "at most 1024 characters")

		d, err := client.Delegate(alex, &authorityv1.DelegateRequest{Module: "org-directory", Scopes: entries("read"), Purpose: "a scheduled job"})
		require.NoError(t, err)
		require.True(t, d.GetActive())
		issue := func(scopes []*authorityv1.WorkScopeV1) (*authorityv1.IssueResponse, error) {
			return client.Issue(ctx, &authorityv1.IssueRequest{
				As:    &authorityv1.IssueRequest_Delegation{Delegation: &authorityv1.DelegationRef{DelegationId: d.GetDelegationId()}},
				Actor: &authorityv1.WorkloadCredential{Credential: workloadCredential}, OperationBindingId: "records", Audience: "records", Scopes: scopes,
			})
		}
		issued, err := issue(nil)
		require.NoError(t, err)
		require.Equal(t, d.GetGrantor(), issued.GetContext().GetOwnerPrincipalId())
		hops := issued.GetContext().GetActorChain()
		require.Len(t, hops, 1)
		require.Equal(t, d.GetDelegationId(), hops[0].GetDelegationId())
		require.Equal(t, []string{"read"}, hops[0].GetGrantedScopes()[0].GetActions())
		_, err = issue(entries("invoke"))
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		_, err = client.Issue(ctx, &authorityv1.IssueRequest{
			As:    &authorityv1.IssueRequest_Delegation{Delegation: &authorityv1.DelegationRef{DelegationId: d.GetDelegationId()}},
			Actor: &authorityv1.WorkloadCredential{Credential: documentsCredential}, OperationBindingId: "model", Audience: "model",
		})
		requireCode(t, err, codes.PermissionDenied, "is granted to org-directory")
		_, err = client.RevokeDelegation(bob, &authorityv1.RevokeDelegationRequest{DelegationId: d.GetDelegationId()})
		requireCode(t, err, codes.NotFound, "delegation")

		own, err := client.Issue(alex, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}},
			Audience: "records", Scopes: entries("read")})
		require.NoError(t, err)
		revoked, err := client.RevokeDelegation(alex, &authorityv1.RevokeDelegationRequest{DelegationId: d.GetDelegationId()})
		require.NoError(t, err)
		require.False(t, revoked.GetDelegation().GetActive())
		again, err := client.RevokeDelegation(alex, &authorityv1.RevokeDelegationRequest{DelegationId: d.GetDelegationId()})
		require.NoError(t, err, "revoking twice answers the delegation as it stands")
		require.False(t, again.GetDelegation().GetActive())
		_, err = issue(nil)
		requireCode(t, err, codes.PermissionDenied, "is revoked")
		_, err = client.Check(ctx, &authorityv1.CheckRequest{Token: issued.GetToken(), Audience: "records"})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "a capability issued before the revocation: %v", err)
		read, err := client.GetDelegation(ctx, &authorityv1.GetDelegationRequest{DelegationId: d.GetDelegationId(), Actor: &authorityv1.WorkloadCredential{Credential: workloadCredential}})
		require.NoError(t, err)
		require.False(t, read.GetActive())
		// The revocation moved the organization's revision, so the grantor's own
		// capability is issued again rather than relied on across it.
		_ = own

		bobs, err := client.Delegate(bob, &authorityv1.DelegateRequest{Module: "org-directory", Scopes: entries("read"), Purpose: "a member's job"})
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
				Scopes: entries("invoke", "read")})
			require.NoError(t, err)
			for _, operation := range order {
				issued, err := client.Issue(ctx, &authorityv1.IssueRequest{
					As:    &authorityv1.IssueRequest_Delegation{Delegation: &authorityv1.DelegationRef{DelegationId: d.GetDelegationId()}},
					Actor: &authorityv1.WorkloadCredential{Credential: workloadCredential}, OperationBindingId: "records", Operation: operation, Audience: "records",
				})
				require.NoError(t, err, "%v: issue under %s", order, operation)
				granted := issued.GetContext().GetActorChain()[0].GetGrantedScopes()
				want := map[string][]string{"invoke": {"invoke", "read"}, "lookup": {"read"}}[operation]
				require.Equal(t, want, granted[0].GetActions(), "%v: the %s issue carries its own scope set", order, operation)
				_, err = client.Check(ctx, &authorityv1.CheckRequest{Token: issued.GetToken(), Audience: "records"})
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
		svc.SetAuthorityInstallStep(func(ctx context.Context, inst domain.Installation) error {
			started <- struct{}{}
			select {
			case <-release:
				return acknowledgeSetup(ctx, inst)
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		t.Cleanup(func() { svc.SetAuthorityInstallStep(acknowledgeSetup) })
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

		// accounts' own record of the installation follows the core's: active,
		// with no install step acknowledged, and exposed to the declared team.
		available, _, err := svc.ListAvailableSolutions(ctx, fixtureOrganization, "", 500)
		require.NoError(t, err)
		var target string
		for _, entry := range available {
			if entry.RouteAlias == "conformance-late" {
				target = entry.TargetID
			}
		}
		require.NotEmpty(t, target)
		rows, _, err := svc.ListOrganizationInstallations(ctx, fixtureOrganization, "", 100)
		require.NoError(t, err)
		var native *gen.Installation
		for _, row := range rows {
			if row.Summary.GetInstallation().GetTargetId() == target {
				native = row.Summary.GetInstallation()
			}
		}
		require.NotNil(t, native, "accounts holds its own record of the installation")
		require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, native.GetStatus(), "accounts' record follows the core's")
		entitled, err := svc.ListSolutionEntitlements(ctx, &gen.ListSolutionEntitlementsRequest{OrgId: fixtureOrganization, SubjectId: conformanceMaya})
		require.NoError(t, err)
		var exposed bool
		for _, entitlement := range entitled.GetEntitlements() {
			exposed = exposed || entitlement.GetInstallationId() == native.GetId()
		}
		require.True(t, exposed, "the declared team is exposed to it")

		// A person on the team is issued a capability through the solution.
		issued, err := authorityv1.NewAuthorityServiceClient(conn).Issue(signIn("fixture-maya"), &authorityv1.IssueRequest{
			As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: "conformance-late"})
		require.NoError(t, err, "a member of the declared team holds the solution's role")
		require.NotEmpty(t, issued.GetContext().GetAuthorityScopes())
	})

	// An upgrade in place is fenced as an activation is: a supporting module
	// its step converged and that is uninstalled before the core records the
	// upgrade refuses it, and the installation stays on its consent.
	t.Run("an upgrade refuses a supporting module revoked between its step and its write", func(t *testing.T) {
		const support, target = "conformance-upgrade-support", "conformance-upgrade-fence"
		deliverModule(t, ctx, conn, signer, conformanceModule{alias: support, installationRole: domain.NoInstallationRole})
		page := wireScope(target+".page", "read", "write")
		read := wireScope(target+".page", "read")
		solution := conformanceSolution{alias: target, generation: 1, installationRole: domain.NoInstallationRole, ceilings: []*authorityv1.WorkScopeV1{page},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {page}, "viewer": {read}},
			edges: []modulecontract.EdgeDependency{{Module: support, Presence: "required"}}}
		deliverSolution(t, ctx, conn, signer, solution)
		install := func() (domain.Installation, error) {
			t.Helper()
			resolved, err := svc.ResolveInstallationPrincipal(ctx, fixtureOrganization, target)
			require.NoError(t, err)
			review, err := svc.ReviewSolutionInstall(ctx, alex, fixtureOrganization, resolved.TargetID)
			require.NoError(t, err)
			installed, err := svc.InstallReviewedSolution(ctx, alex, fixtureOrganization, review, business.InstallAccess{})
			return installed.Installation, err
		}
		installed, err := install()
		require.NoError(t, err)
		require.Equal(t, domain.Active, installed.Status)
		plan, err := store.SolutionInstallationPlan(ctx, fixtureOrganization, installed.ID)
		require.NoError(t, err)
		require.Len(t, plan.Dependencies, 1)
		supporting := plan.Dependencies[0].InstallationID

		solution.generation = 2
		solution.roles["editor"] = []*authorityv1.WorkScopeV1{page}
		deliverSolution(t, ctx, conn, signer, solution)
		svc.SetAuthorityInstallStep(func(ctx context.Context, inst domain.Installation) error {
			upgrade, err := svc.ConvergeReviewedClosure(ctx, inst)
			if err != nil {
				return err
			}
			require.True(t, upgrade)
			// The supporting module's installation is retired between the step
			// and the write, as its withdrawal at this organization would.
			return store.WithOrgTx(ctx, fixtureOrganization, func(ctx context.Context) error {
				_, _, err := store.UninstallSolution(ctx, fixtureOrganization, supporting)
				return err
			})
		})
		t.Cleanup(func() { svc.SetAuthorityInstallStep(acknowledgeSetup) })
		_, err = install()
		require.ErrorContains(t, err, "INSTALLATION_PREREQUISITES_CHANGED")
		all, err := core.Installations(ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		for _, inst := range all {
			if inst.ID == installed.ID {
				require.Equal(t, domain.Active, inst.Status)
				require.Equal(t, uint64(1), inst.Consent.Generation, "the installation stays on the consent it had")
			}
		}
	})

	// A module page that declares roles is installed by composition, as every
	// module is, through the host's own module path, holding no standing
	// authority; it reaches the navigation of a person holding one of its
	// roles on that installation, and of no other member of the organization;
	// its page's audience answers the same way. Redelivered without roles, it
	// leaves the role holder's navigation. Nobody reviews or installs it as a
	// solution, and a module page selecting a standing role is refused.
	t.Run("a module page's roles decide who is listed for it", func(t *testing.T) {
		const module = "conformance-module-page"
		note := wireScope(module+".note", "read")
		page := &solutionhost.Frontend{Title: "Module page", ExposedModule: "./Page", HostContract: 1, Payload: pagePayload}
		withRoles := conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{note}, page: page, installationRole: domain.NoInstallationRole,
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {note}, "viewer": {note}}}
		deliverModule(t, ctx, conn, signer, withRoles)
		_, err := installConformanceSolution(ctx, svc, core, module, "", conformanceAlex)
		code, _ := domain.CodeOf(err)
		require.Equal(t, domain.NotFound, code, "a module page is never reviewed or installed as a solution: %v", err)
		require.NoError(t, consentModule(ctx, svc, store, fixtureOrganization, conformanceAlex, module))
		all, err := core.Installations(ctx, alex, fixtureOrganization)
		require.NoError(t, err)
		i := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.Target == module && inst.Status == domain.Active })
		require.GreaterOrEqual(t, i, 0, "the module page is installed by composition at Acme")
		inst := all[i]
		require.Equal(t, authority.StablePrincipalID(fixtureOrganization, module), inst.PrincipalID, "its installation is the host's own record, owning its page's data (RBAC-1)")
		require.Empty(t, inst.AgentRole, "holding no standing authority")
		const nora, remi = "00000000-0000-7000-8000-0000000000b8", "00000000-0000-7000-8000-0000000000b2"
		_, err = core.GrantRoleAsHost(ctx, authority.RoleRequest{Organization: fixtureOrganization, InstallationID: inst.ID, Role: "viewer", PersonID: nora})
		require.NoError(t, err)
		listed := func(person string) bool {
			t.Helper()
			entitled, err := svc.ListSolutionEntitlements(ctx, &gen.ListSolutionEntitlementsRequest{OrgId: fixtureOrganization, SubjectId: person})
			require.NoError(t, err)
			for _, entitlement := range entitled.GetEntitlements() {
				if entitlement.GetInstallationId() == inst.ID {
					return true
				}
			}
			return false
		}
		require.True(t, listed(nora), "the viewer's navigation lists the module page, from its composition installation")
		require.False(t, listed(remi), "another member's does not")
		issue := func(token string) error {
			_, err := authorityv1.NewAuthorityServiceClient(conn).Issue(signIn(token), &authorityv1.IssueRequest{
				As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: module})
			return err
		}
		require.NoError(t, issue("fixture-no-roles"), "the viewer is issued a capability at the page's audience")
		require.Equal(t, codes.PermissionDenied, status.Code(issue("fixture-remi")), "another member is not")
		sealed, err := svc.WorkContextAudienceInstallation(ctx, fixtureOrganization, module)
		require.NoError(t, err)
		require.Equal(t, inst.ID, sealed, "the host's seal resolves the module page's installation as any module's")

		withoutRoles := withRoles
		withoutRoles.roles, withoutRoles.generation = nil, 2
		deliverModule(t, ctx, conn, signer, withoutRoles)
		require.False(t, listed(nora), "a module page without roles is listed by no role")

		// A module page holds no standing authority: a declaration selecting
		// one of its roles is refused by the host's module install path.
		const selected = "conformance-module-page-selected"
		selectedNote := wireScope(selected+".note", "read")
		deliverModule(t, ctx, conn, signer, conformanceModule{alias: selected, ceilings: []*authorityv1.WorkScopeV1{selectedNote}, page: page, installationRole: "viewer",
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {selectedNote}, "viewer": {selectedNote}}})
		_, _, err = svc.InstallFixtureTarget(ctx, conformanceAlex, fixtureOrganization, selected, "", conformanceAlex)
		require.ErrorContains(t, err, "holds no standing authority")
	})

	closures := reviewedClosureHarness{ctx: ctx, conn: conn, signer: signer, svc: svc, store: store, faults: faults, core: core, pagePayload: pagePayload,
		edgeCredential:   cluster.Run(executiontest.Workload{Namespace: business.HopModule, ServiceAccount: business.ModuleEdgeService, Container: "app"}),
		moduleCredential: cluster.Run(executiontest.Workload{Namespace: "org-directory", ServiceAccount: "org-directory", Container: "app"})}
	testReviewedClosures(t, closures)
	testInstallAssignsAccess(t, closures)
	testModulePagesAndReach(t, closures)
	testModulePageRoleReachesThroughItsBinding(t, closures)
	testPagesReachThroughTheAgent(t, closures)
	testAnEditorSavesAndIsOfferedTheRunnablesTheirRoleAllows(t, closures)
	testModulesInstalledByComposition(t, closures)
	testSolutionSourcesAreGovernedByTheirScope(t, closures)

	// A fixture's solution row runs inside a delivery pass, so its install
	// step never waits on the solution's setup: a pass with a setup that has
	// not answered completes at once, the row pending, and a later pass, once
	// the setup answered, activates the same attempt.
	t.Run("a pass with a pending fixture solution row completes at once; the row converges on a later pass", func(t *testing.T) {
		const solution, org, owner = "conformance-fixture-pending", "00000000-0000-7000-8000-0000000001c7", "00000000-0000-7000-8000-0000000001f7"
		note := wireScope(solution+".note", "read")
		deliverSolution(t, ctx, conn, signer, conformanceSolution{alias: solution, generation: 1, installationRole: domain.NoInstallationRole,
			ceilings: []*authorityv1.WorkScopeV1{note}, roles: map[string][]*authorityv1.WorkScopeV1{"installer": {note}, "viewer": {note}}})
		path := filepath.Join(t.TempDir(), "pending-fixture.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`users:
  - {id: `+owner+`, email: owner@west.example.test, name: West Owner, role: member, provider: email, provider_id: fixture-west-owner}
organizations:
  - {id: `+org+`, name: Example West, owner: owner@west.example.test, members: []}
installations:
  - {org: Example West, target: `+solution+`, role: viewer, owner: owner@west.example.test}
`), 0o600))
		t.Setenv("DEV_FIXTURE_PATH", path)
		// The host's own setup step: nothing answers its job yet.
		svc.SetAuthorityInstallStep(nil)
		t.Cleanup(func() { svc.SetAuthorityInstallStep(acknowledgeSetup) })
		rows, err := fixtures.SeedWithInstallations(ctx, svc, "pending-fixture")
		require.NoError(t, err)
		start := time.Now()
		require.NoError(t, rows.Converge(ctx), "a setup that has not answered yet fails no pass")
		require.Less(t, time.Since(start), 5*time.Second, "the pass waited on the setup")
		west := authority.Person{ID: owner, Organization: org}
		all, err := core.Installations(ctx, west, org)
		require.NoError(t, err)
		i := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.Target == solution })
		require.GreaterOrEqual(t, i, 0)
		require.Equal(t, domain.Pending, all[i].Status)
		// The setup answers (the harness's step acknowledges it); the next
		// pass activates the same attempt.
		svc.SetAuthorityInstallStep(acknowledgeSetup)
		require.NoError(t, rows.Converge(ctx))
		all, err = core.Installations(ctx, west, org)
		require.NoError(t, err)
		i = slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.Target == solution })
		require.Equal(t, domain.Active, all[i].Status)
	})

	// A fixture installs as an administrator does in the admin area: a solution
	// through its reviewed install and a module page by composition then its Set
	// up, each declared role to its `<target>-<role>` team, the installer joining
	// the teams of the roles they take, and each declared member joining their
	// role's team. A person holds nothing else, and a second pass changes nothing.
	t.Run("a fixture installs like an administrator: each role to its team, the installer and the members joining theirs", func(t *testing.T) {
		const solution, module, org = "conformance-fixture-admin", "conformance-fixture-admin-page", "00000000-0000-7000-8000-0000000002d1"
		const owner, editor, viewer = "00000000-0000-7000-8000-0000000002d2", "00000000-0000-7000-8000-0000000002d3", "00000000-0000-7000-8000-0000000002d4"
		note := wireScope(solution+".note", "read", "write")
		deliverSolution(t, ctx, conn, signer, conformanceSolution{alias: solution, generation: 1, installationRole: domain.NoInstallationRole,
			ceilings: []*authorityv1.WorkScopeV1{note}, roles: map[string][]*authorityv1.WorkScopeV1{
				"installer": {note}, "editor": {note}, "viewer": {wireScope(solution+".note", "read")}}})
		entry := wireScope(module+".entry", "read")
		deliverModule(t, ctx, conn, signer, conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{entry}, installationRole: domain.NoInstallationRole,
			page:  &solutionhost.Frontend{Title: "Fixture page", ExposedModule: "./Page", HostContract: 1, Payload: pagePayload},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {entry}, "viewer": {entry}}})
		path := filepath.Join(t.TempDir(), "admin-fixture.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`users:
  - {id: `+owner+`, email: owner@admin-fixture.example.test, name: Admin Fixture Owner, role: member, provider: email, provider_id: fixture-admin-fixture-owner}
  - {id: `+editor+`, email: editor@admin-fixture.example.test, name: Admin Fixture Editor, role: member, provider: email, provider_id: fixture-admin-fixture-editor}
  - {id: `+viewer+`, email: viewer@admin-fixture.example.test, name: Admin Fixture Viewer, role: member, provider: email, provider_id: fixture-admin-fixture-viewer}
organizations:
  - id: `+org+`
    name: Example Admin Fixture
    owner: owner@admin-fixture.example.test
    members:
      - {email: editor@admin-fixture.example.test, role: member}
      - {email: viewer@admin-fixture.example.test, role: member}
installations:
  - org: Example Admin Fixture
    target: `+solution+`
    owner: owner@admin-fixture.example.test
    installer_roles: [installer]
    members: {editor: [editor@admin-fixture.example.test], viewer: [viewer@admin-fixture.example.test]}
  - org: Example Admin Fixture
    target: `+module+`
    owner: owner@admin-fixture.example.test
    installer_roles: [installer]
    members: {viewer: [editor@admin-fixture.example.test, viewer@admin-fixture.example.test]}
`), 0o600))
		t.Setenv("DEV_FIXTURE_PATH", path)
		rows, err := fixtures.SeedWithInstallations(ctx, svc, "admin-fixture")
		require.NoError(t, err)
		require.NoError(t, rows.Converge(ctx))
		require.NoError(t, rows.Converge(ctx), "a converged row changes nothing")

		installer := authority.Person{ID: owner, Organization: org}
		teamsOf := func(member string) []string {
			t.Helper()
			var teams []*gen.Team
			require.NoError(t, svc.Store().WithControlPlane(ctx, func(ctx context.Context) error {
				var err error
				teams, err = svc.Store().ListTeams(ctx, org, member)
				return err
			}))
			var names []string
			for _, team := range teams {
				names = append(names, team.GetName())
			}
			slices.Sort(names)
			return names
		}
		require.Equal(t, []string{solution + "-editor", module + "-viewer"}, teamsOf(editor))
		require.Equal(t, []string{module + "-viewer", solution + "-viewer"}, teamsOf(viewer))
		require.Equal(t, []string{solution + "-installer", module + "-installer"}, teamsOf(owner), "the installer joins the teams of the roles they take, and only those")
		require.Len(t, teamsOf(""), 5, "one team per declared role, created by the install and nothing else")

		for target, roles := range map[string][]string{solution: {"editor", "installer", "viewer"}, module: {"installer", "viewer"}} {
			id := authority.StableInstallationID(org, target)
			if target == module {
				id, err = svc.WorkContextAudienceInstallation(ctx, org, module)
				require.NoError(t, err)
			}
			grants, err := core.ListRoleGrants(ctx, installer, org, id)
			require.NoError(t, err)
			var held []string
			for _, grant := range grants {
				require.Empty(t, grant.PersonID, "%s: a role is granted to a team, never to a person", target)
				held = append(held, grant.Role)
			}
			slices.Sort(held)
			require.Equal(t, roles, held, "%s: each declared role to its one team", target)
		}
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
	server := &http.Server{Handler: mux, Protocols: unencryptedHTTP2(), ReadHeaderTimeout: 10 * time.Second,
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

// These test owners stand in for the platform's delivery account and approval:
// "delivery:<namespace>", or a workload credential the harness delivers that
// namespace with (workloads).
type conformanceCarrier struct{ workloads map[string]string }

func (c conformanceCarrier) AuthorizeCarrier(_ context.Context, credential string, namespaces ...string) error {
	for _, namespace := range namespaces {
		if credential == "Bearer delivery:"+namespace {
			return nil
		}
		if workload, ok := c.workloads[namespace]; ok && credential == "Bearer "+workload {
			return nil
		}
	}
	return fmt.Errorf("not the delivery workload of %v", namespaces)
}
