package adapters

// The harness of rung 1: accounts, booted over its test store with the dev-admin
// fixture, serves the host interfaces over an in-memory connection and the
// interface conformance suite drives them. Every red line is a line of the gap
// list in HOST_INTERFACE.md; the suite is never loosened to make a line green.

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	conformance "github.com/codefly-dev/authority/libraries/conformance/go"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/solutionhost"
	codefly "github.com/codefly-dev/sdk-go"
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
	Package "accounts/pkg/auth/dev"
	ed25519minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/business/compositiontest"
	"accounts/pkg/business/executiontest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	"accounts/pkg/infra"
)

// fixtureOrganization is Acme Corp in the harness's fixture, and
// conformanceAlex one of its administrators.
const (
	fixtureOrganization = "00000000-0000-7000-8000-0000000000c1"
	conformanceAlex     = "00000000-0000-7000-8000-0000000000b3"
)

func TestAccountsPassesTheHostInterfaceConformanceSuite(t *testing.T) {
	if os.Getenv("HOST_INTERFACE_CONFORMANCE") == "" {
		t.Skip("set HOST_INTERFACE_CONFORMANCE=1: boots the test store and seeds the dev-admin fixture (rung 1 harness)")
	}
	runHostInterfaceFixture(t, true)
}

func TestFreshStoreDerivesManifestAuthorityAndIssuesAPerson(t *testing.T) {
	runHostInterfaceFixture(t, false)
}

func runHostInterfaceFixture(t *testing.T, fullSuite bool) {
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

	// The composition: two module principals the host knows, as the module
	// registry would hold them after delivery.
	// documents is the audience the viewer reads at; org-directory is the page
	// module that runs as one declared workload and reads documents for the
	// viewer through its one binding; model is an audience the viewer's context
	// at documents is not addressed to. The fake cluster projects the credential
	// a pod of org-directory's workload would present.
	// documents and model also run as workloads, each with a binding to the
	// other, so a delegate chain crosses from one to the other and back.
	// conformance-agent is a module whose tools binding reaches an installed
	// solution; conformance-idle is a declared workload with no active
	// installation anywhere; model also binds to itself across both its kinds
	// (a self binding admits itself headless, as a module's own trigger does).
	reads := []business.ModuleOperationScope{{ResourceKind: "example-records", Actions: []string{"invoke", "read"}}}
	lookups := []business.ModuleOperationScope{{ResourceKind: "example-records", Actions: []string{"read"}}}
	invokes := []business.ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke", "read"}}}
	modelSelf := []business.ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke", "read"}}, {ResourceKind: "modelservice.datasets", Actions: []string{"read"}}}
	tools := []business.ModuleOperationScope{{ResourceKind: "conformance-proposals.item", Actions: []string{"read"}}}
	idle := []business.ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"read"}}}
	registry := business.ModulePrincipalRegistry{
		business.ModulePrincipalID("documents"): {
			Prefix: "documents", Resources: []string{"example-records"}, Tenant: fixtureOrganization,
			Workloads: []business.ModuleWorkload{{Namespace: "documents", ServiceAccount: "documents", Container: "app"}},
			OperationAudiences: map[string]business.ModuleOperationAudience{
				"model": {Audience: "model", InvokeScopes: invokes},
			},
		},
		business.ModulePrincipalID("org-directory"): {
			Prefix: "org-directory", Resources: []string{"directory.entry"}, Tenant: fixtureOrganization,
			Workloads: []business.ModuleWorkload{{Namespace: "org-directory", ServiceAccount: "org-directory", Container: "app"}},
			OperationAudiences: map[string]business.ModuleOperationAudience{
				"documents": {Audience: "documents", InvokeScopes: reads, LookupScopes: lookups, HeadlessScopes: reads},
			},
		},
		business.ModulePrincipalID("model"): {
			Prefix: "model", Resources: []string{"modelservice.profiles", "modelservice.datasets"}, Tenant: fixtureOrganization,
			Workloads: []business.ModuleWorkload{{Namespace: "model", ServiceAccount: "model", Container: "app"}},
			OperationAudiences: map[string]business.ModuleOperationAudience{
				"records": {Audience: "documents", InvokeScopes: reads, LookupScopes: lookups},
				"self":    {Audience: "model", InvokeScopes: modelSelf, HeadlessScopes: modelSelf},
			},
		},
		business.ModulePrincipalID("conformance-agent"): {
			Prefix: "conformance-agent", Resources: []string{"conformance-agent.conversation"},
			Workloads: []business.ModuleWorkload{{Namespace: "conformance-agent", ServiceAccount: "conformance-agent", Container: "app"}},
			OperationAudiences: map[string]business.ModuleOperationAudience{
				"tools": {Audience: "conformance-proposals", InvokeScopes: tools},
			},
		},
		business.ModulePrincipalID("conformance-idle"): {
			Prefix: "conformance-idle", Resources: []string{"conformance-idle.task"},
			Workloads: []business.ModuleWorkload{{Namespace: "conformance-idle", ServiceAccount: "conformance-idle", Container: "app"}},
			OperationAudiences: map[string]business.ModuleOperationAudience{
				"model": {Audience: "model", InvokeScopes: idle, HeadlessScopes: idle},
			},
		},
	}
	cluster := executiontest.New()
	svc.SetExecutionBinding(cluster)
	workloadCredential := cluster.Run(executiontest.Workload{Namespace: "org-directory", ServiceAccount: "org-directory", Container: "app"})
	documentsCredential := cluster.Run(executiontest.Workload{Namespace: "documents", ServiceAccount: "documents", Container: "app"})
	modelCredential := cluster.Run(executiontest.Workload{Namespace: "model", ServiceAccount: "model", Container: "app"})
	credential := func(name string) string {
		return cluster.Run(executiontest.Workload{Namespace: name, ServiceAccount: name, Container: "app"})
	}
	// The install steps are queued on the host's own job store, which the
	// harness claims and finishes as a gateway replica does.
	jobPool, err := infra.NewJobWorkerPool(ctx)
	require.NoError(t, err)
	t.Cleanup(jobPool.Close)
	svc.SetModuleCapabilities(store, infra.NewPostgresJobStore(jobPool), nil)
	// Uninstalling narrows authority, which the host does only once the policy
	// log witnesses it; the log is external, so it stands in at its seam.
	svc.SetPolicyLog(&memoryPolicyLog{}, store)

	// Composition installs a module at every organisation (contract decision of
	// 2026-10-22): the live module authority answers for every declared principal.
	composed := compositiontest.NewComposedModuleInstallations(svc, store)
	svc.SetModuleAuthorityReads(withdrawnModuleInstallations{ComposedModuleInstallations: composed, withdrawn: "conformance-idle"}, nil)
	svc.SetWorkContextSealStore(composed)

	// Signed delivery uses the production inbox, verifier and reconciler. The
	// fixture's ceiling is explicit; no warn mode or unsigned sketch is used.
	signer := deliverytest.Signer(t, "conformance-release")
	verifier := deliverytest.Verifier(t, map[string][]string{signer.KeyID(): {"acme"}})
	svc.SetSolutionDelivery(verifier, store, conformanceCarrier{})
	svc.SetSolutionDeliveryCoordinate("conformance-host")
	svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
	})
	svc.SetModuleEnvelopeStore(store)
	require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
		record, err := store.LatestModuleEnvelopeRecord(ctx)
		require.Nil(t, record, "the fresh host has no administrative envelope")
		return err
	}))
	require.Empty(t, svc.ModulePrincipals())
	reconciler, err := business.NewSolutionHostBindingReconciler(svc, business.SolutionHostBindingReconcilerConfig{
		Source: business.NewDeliveredSolutionHostBindings(svc), Verifier: verifier, Coordinate: "conformance-host",
	})
	require.NoError(t, err)
	svc.SetCompositionReconcile(reconciler.RunOnce)
	for _, grant := range registry {
		m := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: grant.Prefix, Alias: grant.Prefix, Kind: solutionhost.KindModule, Generation: 1,
			Host: solutionhost.HostTarget{Coordinate: "conformance-host", Domain: "acme"}, Release: solutionhost.Release{Publisher: "conformance", Name: grant.Prefix, Version: "0.1.0"},
			Workloads: []solutionhost.Workload{{Service: grant.Prefix, Container: "app"}}, Authority: &solutionhost.Authority{Principal: grant.Prefix, ResourceKinds: grant.Resources}}
		for name, binding := range grant.OperationAudiences {
			operations := map[string][]string{}
			for operation, scopes := range map[string][]business.ModuleOperationScope{"invoke": binding.InvokeScopes, "headless": binding.HeadlessScopes, "lookup": binding.LookupScopes} {
				for _, scope := range scopes {
					for _, action := range scope.Actions {
						operations[operation] = append(operations[operation], scope.ResourceKind+":"+action)
					}
				}
			}
			m.Authority.Bindings = append(m.Authority.Bindings, solutionhost.AuthorityBinding{ID: name, Audience: binding.Audience, Operations: operations})
		}
		doc, err := solutionhost.SignedPayload(m)
		require.NoError(t, err)
		_, _, err = svc.ReceiveSolutionDelivery(ctx, deliverytest.Carrier(t, doc, signer.KeyID()), "Bearer delivery:"+grant.Prefix)
		require.NoError(t, err)
	}
	require.NoError(t, reconciler.RunOnce(ctx))
	require.Len(t, svc.ModulePrincipals(), len(registry), "signed manifests derive every principal without an envelope")

	previous := service
	service = svc
	t.Cleanup(func() { service = previous })

	// The servers, as production builds them; then the Work Context authority
	// configured against this store with a test key (NewGrpServer configures it
	// from the permissions plugin, which a test has none of).
	grpcServer, err := NewGrpServer(&Configuration{})
	require.NoError(t, err)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authority, _ := svc.Store().(business.WorkContextAuthorityStore)
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{
		Issuer: "https://accounts.conformance.test", KeyID: "conformance-1", PrivateKey: key, Authority: authority, Seals: svc,
	})
	require.NoError(t, workContextSingleton.configureErr)

	// The Connect listener is the catalog-complete one (business-backed services
	// are Connect-only) and speaks the gRPC protocol over h2c, so the suite dials
	// it with a plain gRPC client on an ephemeral port.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := uint16(probe.Addr().(*net.TCPAddr).Port)
	require.NoError(t, probe.Close())
	connectServer, err := NewConnectServer(&Configuration{EndpointConnectPort: &port}, grpcServer)
	require.NoError(t, err)
	serveCtx, stopServing := context.WithCancel(ctx)
	t.Cleanup(stopServing)
	go func() { _ = connectServer.Run(serveCtx) }()
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 20*time.Second, 100*time.Millisecond, "the Connect listener did not come up")

	if fullSuite {
		// The solutions, delivered as the CLI delivers them, then installed by
		// the fixture's declared installations: each is pending until its
		// install step is acknowledged, and exposed to its teams after.
		// conformance-pages carries the roles, routes and binding the suite's
		// solution-role cases deliver at the next generation.
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
		// The first pass installs; a team is not exposed to a pending
		// installation, so its exposures wait for the second pass, which must
		// then converge every declared installation.
		if err := installations.Converge(ctx); err != nil {
			t.Logf("installations before their install steps are acknowledged: %v", err)
		}
		require.NoError(t, acknowledgeInstallSteps(ctx, svc))
		require.NoError(t, installations.Converge(ctx), "acknowledged installations are exposed to their teams")
		pages := installationOf(t, ctx, conn, "fixture-alex", "conformance-pages")
		assistant := installationOf(t, ctx, conn, "fixture-alex", "conformance-assistant")
		pagesCredential, assistantCredential := credential("conformance-pages"), credential("conformance-assistant")

		// A delegate chain (gap 14): an administrator issued at documents;
		// documents hops to model through its binding for what the first token
		// never carried, then model hops back to documents through its own.
		chain := conformance.Chain{Token: "fixture-alex", Start: "documents", Hops: []conformance.Hop{
			{Credential: documentsCredential, Binding: "model", Audience: "model", AskKind: "modelservice.profiles", AskAction: "invoke"},
			{Credential: modelCredential, Binding: "records", Audience: "documents", AskKind: "example-records", AskAction: "read"},
		}}
		conformance.Run(t, conn, registeredDescriptors(t), conformance.Fixture{
			Organization:   fixtureOrganization,
			HostCoordinate: "conformance-host", HostDomain: "acme",
			SignManifest:       func(doc []byte) ([]byte, error) { return solutionhost.SignEnvelope(doc, signer) },
			SignAuthority:      func(doc []byte) ([]byte, error) { return delivery.Sign(doc, signer.KeyID(), signer.Sign) },
			DeliveryCredential: func(module string) string { return "delivery:" + module },
			ViewerToken:        "dev-bob", AdminToken: "dev-alice",
			Audience: "documents", ReadKind: "example-records",
			// The credential a pod of org-directory's declared workload presents.
			WorkloadModule: "org-directory", WorkloadCredential: workloadCredential, Binding: "documents", BindingAudience: "documents", Elsewhere: "model",
			// The installed pages solution and its own workload.
			InstalledSolution: pages, InstalledAudience: "conformance-pages", InstalledCredential: pagesCredential,
			UnexposedTeam: teamNamed(t, ctx, svc, fixtureOrganization, "Unexposed"),
			// An Acme owner who is not an administrator; a member with no role and no team.
			OwnerToken: "fixture-owner", UnentitledToken: "fixture-no-roles",
			// Maya, on finance, views the pages; Nora holds no role there. The
			// viewer role writes example records at documents through the
			// solution's binding, never on its own.
			SolutionRoles: &conformance.SolutionRoleCase{
				Solution: "conformance-pages", Credential: pagesCredential, Token: "fixture-maya", Outsider: "fixture-no-roles",
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
			InstallationChain: &conformance.InstallationChainCase{Installation: assistant, Source: "conformance-assistant", Credential: assistantCredential,
				Hops: []conformance.Hop{
					{Credential: assistantCredential, Binding: "directory", Audience: "org-directory", AskKind: "directory.entry", AskAction: "read"},
					{Credential: workloadCredential, Binding: "documents", Audience: "documents", AskKind: "example-records", AskAction: "read"},
					{Credential: documentsCredential, Binding: "model", Audience: "model", AskKind: "modelservice.profiles", AskAction: "invoke"},
				}},
			// The suite delivers conformance-lifecycle and installs it; its step
			// waits until the harness acknowledges it, and uninstalling revokes it.
			Lifecycle: &conformance.InstallationLifecycleCase{
				Target: "conformance-lifecycle", Credential: credential("conformance-lifecycle"), Kind: "conformance-lifecycle.page",
				Team: teamNamed(t, ctx, svc, fixtureOrganization, "Finance"),
				PauseInstall: func() func() {
					var once sync.Once
					return func() { once.Do(func() { require.NoError(t, acknowledgeInstallSteps(ctx, svc)) }) }
				},
				Revoke: func(ctx context.Context, id string) error {
					return svc.UninstallSolution(ctx, conformanceAlex, fixtureOrganization, id)
				},
			},
			// The permission matrix over the composed audiences, as the dev-admin
			// fixture states it: a collection reader, Acme administrators (one of
			// them a person of the authority's fixture), a member who holds no
			// role, and a Globex member who holds nothing at Acme's content.
			Chain: &chain,
			// The same binding read as a lookup carries its lookup scopes alone.
			Lookup: &conformance.LookupCase{Token: "fixture-alex", Start: "model", Credential: modelCredential, Binding: "records",
				Audience: "documents", Kind: "example-records", Invoke: "invoke", Lookup: "read"},
			Matrix: []conformance.Expectation{
				{Token: "dev-bob", Audience: "documents", Kind: "example-records", Action: "read", Allowed: true},
				{Token: "dev-bob", Audience: "documents", Kind: "example-records", Action: "write", Allowed: false},
				{Token: "dev-alice", Audience: "documents", Kind: "example-records", Action: "write", Allowed: true},
				{Token: "fixture-alex", Audience: "model", Kind: "modelservice.profiles", Action: "invoke", Allowed: true},
				{Token: "fixture-maya", Audience: "model", Kind: "modelservice.profiles", Action: "invoke", Allowed: false},
				{Token: "fixture-omar", Audience: "documents", Kind: "example-records", Action: "read", Allowed: false},
			},
		})

	}

	t.Run("a signed solution delivery governs its HTTP routes", func(t *testing.T) {
		const audience = "conformance-routes"
		m := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: audience, Alias: audience, Kind: solutionhost.KindSolution, Generation: 1,
			Host: solutionhost.HostTarget{Coordinate: "conformance-host", Domain: "acme"}, Release: solutionhost.Release{Publisher: "conformance", Name: audience, Version: "0.1.0"},
			Workloads: []solutionhost.Workload{{Service: "api", Container: "app"}}, Authority: &solutionhost.Authority{Principal: audience, ResourceKinds: []string{"example-records"}}}
		raw, err := solutionhost.SignedPayload(m)
		require.NoError(t, err)
		// These are the rows Codefly derives from x-codefly-authority-policy on
		// HTTP operations: the same signed sidecar as protobuf method policies.
		doc, err := proto.Marshal(&compositionv1.AuthorityManifestV1{Module: audience, Generation: 1, CoreManifestDigest: delivery.Digest(raw), Methods: []*compositionv1.MethodPolicyV1{
			{Method: "GET /v1/example/pages/{id}", Requires: []*authorityv1.WorkScopeV1{{ResourceKind: "example-records", Actions: []string{"read"}}}},
			{Method: "POST /v1/example/pages/{id}:publish", Requires: []*authorityv1.WorkScopeV1{{ResourceKind: "example-records", Actions: []string{"write"}}}},
			{Method: "POST /v1/example/modules/{path=**}", AnyCheckedCaller: true},
			{Method: "GET /v1/example/unmarked"},
		}})
		require.NoError(t, err)
		envelope, err := delivery.Sign(doc, signer.KeyID(), signer.Sign)
		require.NoError(t, err)
		_, err = compositionv1.NewCompositionServiceClient(conn).ApplyManifest(ctx, &compositionv1.ApplyManifestRequest{
			Carrier:   &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, signer.KeyID())},
			Authority: &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope}, Credential: "delivery:" + audience,
		})
		require.NoError(t, err)
		// An altered authority document at the same generation cannot narrow or
		// widen the committed declaration, even when the replacement is signed.
		changed := &compositionv1.AuthorityManifestV1{}
		require.NoError(t, proto.Unmarshal(doc, changed))
		changed.Methods[0].Requires[0].Actions = []string{"write"}
		altered, err := proto.Marshal(changed)
		require.NoError(t, err)
		alteredSignature, err := delivery.Sign(altered, signer.KeyID(), signer.Sign)
		require.NoError(t, err)
		_, err = compositionv1.NewCompositionServiceClient(conn).ApplyManifest(ctx, &compositionv1.ApplyManifestRequest{
			Carrier:   &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, signer.KeyID())},
			Authority: &compositionv1.SignedManifestV1{Document: altered, Envelope: alteredSignature}, Credential: "delivery:" + audience,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "generation")
		// A person's capability at a solution is sealed to the organisation's
		// installation of it, so the solution is installed, through the host's
		// own install path and acknowledged, before anyone is issued there.
		_, installed, err := svc.InstallTargetByName(ctx, conformanceAlex, fixtureOrganization, audience, "Collection reader", conformanceAlex)
		require.NoError(t, err)
		require.True(t, installed)
		require.NoError(t, acknowledgeInstallSteps(ctx, svc))
		sess, err := sessionv1.NewSessionServiceClient(conn).SignIn(ctx, &sessionv1.SignInRequest{FixtureToken: "dev-bob"})
		require.NoError(t, err)
		bearer := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+sess.GetBearer())
		client := authorityv1.NewAuthorityServiceClient(conn)
		issued, err := client.Issue(bearer, &authorityv1.IssueRequest{OrganizationId: fixtureOrganization, Audience: audience,
			As:     &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}},
			Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "example-records", Actions: []string{"read"}}},
		})
		require.NoError(t, err)
		for _, tc := range []struct {
			method string
			code   codes.Code
		}{
			{"GET /v1/example/pages/one", codes.OK},
			{"POST /v1/example/modules/example.Service/Read", codes.OK},
			{"POST /v1/example/pages/one:publish", codes.PermissionDenied},
			{"GET /v1/example/unmarked", codes.PermissionDenied},
			{"GET /v1/example/missing", codes.NotFound},
			{"GET /v1/example/modules/example.Service/Read", codes.NotFound},
		} {
			_, err := client.Check(ctx, &authorityv1.CheckRequest{Token: issued.GetToken(), Audience: audience, Method: tc.method})
			require.Equal(t, tc.code, status.Code(err), "%s: %v", tc.method, err)
			if tc.code != codes.OK {
				require.Contains(t, status.Convert(err).Message(), tc.method)
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
