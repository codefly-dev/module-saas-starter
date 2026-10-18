//go:build !pure

package infra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// THE MODULE REGISTRY, DERIVED FROM THE APPLIED MODULE MANIFESTS, through real
// Postgres.
//
// Each test drives the host's own path: a platform super_admin writes the
// module envelope through the business method the RPC calls, the module's
// manifest is POSTed through the real delivery handler, and the registry is
// read back through the service every capability decision reads — with no
// service rebuilt between the steps.
//
// The envelope is HOST-WIDE and append-only, like the inbox: each test writes
// the next revision after whatever is current, and asserts only on its own
// principal.

// registryBinding is one deployed instance of the scenario's module.
type registryBinding struct {
	solutionID string
	binding    string
}

func newRegistryBinding(t *testing.T, label string) *registryBinding {
	t.Helper()
	solutionID := uniqueAlias(label)
	return &registryBinding{solutionID: solutionID, binding: "acme.test." + solutionID}
}

// workload is the identity the binding's one workload runs as, by the cell's
// rule.
func (b *registryBinding) workload() business.ModuleWorkload {
	return business.ModuleWorkload{Namespace: productionPathNamespace(b.solutionID), ServiceAccount: "api", Container: "api"}
}

// registryScenario is one module on one host: its principal, its binding, and
// the reconciler deriving the registry.
type registryScenario struct {
	delivery   *productionPathDelivery
	reconciler *business.SolutionHostBindingReconciler
	admin      string
	prefix     string
	tenant     string
	resource   string
	primary    *registryBinding
}

func newRegistryScenario(t *testing.T, label string) *registryScenario {
	t.Helper()
	delivery := newRegistryDelivery(t)
	scenario := &registryScenario{delivery: delivery, admin: seedUser(t), primary: newRegistryBinding(t, label)}
	seedHumanPrincipal(t, scenario.admin, "Jane Doe")
	scenario.tenant = seedOrg(t, scenario.admin)
	require.NoError(t, testStore.As(business.Identity{OrgID: scenario.tenant}).AddOrgMember(testCtx, scenario.admin, "admin"))
	// The module's principal is its own name: the manifest's alias.
	scenario.prefix = scenario.primary.solutionID
	scenario.resource = scenario.prefix + ".reports"
	grantPlatformRoleForTest(t, scenario.admin, "super_admin")
	scenario.reconciler = newRegistryReconciler(t, delivery.service, productionVerifier(t), delivery.service)
	return scenario
}

// newRegistryDelivery is a host replica: the real delivery path, the envelope
// store and the live authority read every capability decision makes.
func newRegistryDelivery(t *testing.T) *productionPathDelivery {
	t.Helper()
	delivery := newProductionPathDelivery(t)
	delivery.service.SetModuleEnvelopeStore(testStore)
	delivery.service.SetModuleAuthorityReads(testStore, nil)
	return delivery
}

func newRegistryReconciler(
	t *testing.T, service *business.Service, verifier *solutionhost.Verifier, envelopes business.ModuleEnvelopeSource,
) *business.SolutionHostBindingReconciler {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(service, business.SolutionHostBindingReconcilerConfig{
		Source:     business.NewDeliveredSolutionHostBindings(service),
		Verifier:   verifier,
		Coordinate: productionPathCoordinate,
		// An hour: a pass these tests observe is one they ran or one a
		// delivery triggered, never one the clock happened to deliver.
		Interval:  time.Hour,
		Envelopes: envelopes,
	})
	require.NoError(t, err)
	return reconciler
}

func (s *registryScenario) principalID() string { return business.ModulePrincipalID(s.prefix) }

func (s *registryScenario) caller(tenant string) business.ModuleCaller {
	return business.ModuleCaller{PrincipalID: s.principalID(), BoundOrg: tenant}
}

func entryOn(service *business.Service, prefix string) (business.ModulePrincipalGrant, bool) {
	grant, ok := service.ModulePrincipals()[business.ModulePrincipalID(prefix)]
	return grant, ok
}

func (s *registryScenario) entry() (business.ModulePrincipalGrant, bool) {
	return entryOn(s.delivery.service, s.prefix)
}

// currentRevision is the envelope revision a write must replace.
func (s *registryScenario) currentRevision(t *testing.T) uint64 {
	t.Helper()
	record, err := s.delivery.service.GetModuleEnvelope(testCtx, s.admin)
	require.NoError(t, err)
	if record == nil {
		return 0
	}
	return record.Revision
}

// envelopeOptions is what one revision of the ceiling grants.
type envelopeOptions struct {
	crossTenant bool
	// resources lets the module's own content kind through the grant.
	resources bool
	// tenant overrides the scenario's tenant.
	tenant string
	// none writes a ceiling with no entry for the scenario's principal.
	none bool
}

func (s *registryScenario) envelope(t *testing.T, revision uint64, options envelopeOptions) []byte {
	t.Helper()
	tenant := s.tenant
	if options.tenant != "" {
		tenant = options.tenant
	}
	grant := map[string]any{
		"tenant":       tenant,
		"cross_tenant": options.crossTenant,
		"queues":       []string{"reports.render"},
		"namespaces":   []string{"reports"},
		"operation_audiences": map[string]any{
			"run": map[string]any{
				"audience":      "example-producer",
				"invoke_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []string{"read", "write"}}},
				"lookup_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []string{"read"}}},
			},
		},
	}
	if options.resources {
		grant["resources"] = []string{s.resource}
	}
	principals := map[string]any{}
	if !options.none {
		principals[s.prefix] = map[string]any{"grant": grant}
	}
	raw, err := json.Marshal(map[string]any{"revision": revision, "principals": principals})
	require.NoError(t, err)
	return raw
}

// putEnvelope writes the next revision as the super_admin and returns it.
func (s *registryScenario) putEnvelope(t *testing.T, options envelopeOptions) uint64 {
	t.Helper()
	current := s.currentRevision(t)
	record, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, s.envelope(t, current+1, options))
	require.NoError(t, err)
	require.Equal(t, current+1, record.Revision)
	return record.Revision
}

// manifest is the module's manifest for one binding at generation: one
// workload, and an authority request for the `run` binding, its queue,
// namespace and resource kind.
func (s *registryScenario) manifest(t *testing.T, binding *registryBinding, generation uint64) *solutionhost.ModuleManifest {
	t.Helper()
	manifest := productionPathManifest(t, binding.solutionID, generation)
	manifest.Kind = solutionhost.KindModule
	manifest.Authority = &solutionhost.Authority{
		Principal: s.prefix,
		Bindings: []solutionhost.AuthorityBinding{{ID: "run", Audience: "example-producer", Operations: map[string][]string{
			"invoke": {"profiles:read", "profiles:write"}, "lookup": {"profiles:read"},
		}}},
		Queues:        []string{"reports.render"},
		Namespaces:    []string{"reports"},
		ResourceKinds: []string{s.resource},
	}
	require.NoError(t, manifest.Validate())
	return manifest
}

func (s *registryScenario) deliverManifest(t *testing.T, binding *registryBinding, generation uint64) {
	t.Helper()
	requireDelivered(t, s.delivery.deliver(t, s.manifest(t, binding, generation)))
}

// established is the common start: an envelope granting the principal, its
// manifest applied and derived, an installation, and a passing check. It
// returns the envelope revision.
func (s *registryScenario) established(t *testing.T) uint64 {
	t.Helper()
	revision := s.putEnvelope(t, envelopeOptions{resources: true})
	s.deliverManifest(t, s.primary, 1)
	s.pass(t)
	_, present := s.entry()
	require.True(t, present, "the module must be in the registry before anything can take it out")
	s.install(t, s.tenant)
	_, err := s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.NoError(t, err, "and a capability check must pass")
	return revision
}

// passReasons runs one reconcile pass and returns its refusals. Refusals
// naming other tests' principals are expected — the inbox is host-wide.
func (s *registryScenario) passReasons(t *testing.T) error {
	t.Helper()
	return s.reconciler.RunOnce(testCtx)
}

// pass runs one reconcile pass and requires that its inputs were readable and
// that none of its refusals is this scenario's own principal.
func (s *registryScenario) pass(t *testing.T) {
	t.Helper()
	if err := s.passReasons(t); err != nil {
		require.NotErrorIs(t, err, business.ErrModulePrincipalInputsUnreadable)
		require.NotContains(t, err.Error(), `"`+s.prefix+`"`, "the pass refused this scenario's own principal")
	}
}

func (s *registryScenario) install(t *testing.T, org string) *gen.Installation {
	t.Helper()
	available, _, err := s.delivery.service.ListAvailableSolutions(testCtx, org, "", 500)
	require.NoError(t, err)
	var target string
	for _, entry := range available {
		if entry.BindingID == s.primary.binding {
			target = entry.TargetID
		}
	}
	require.NotEmpty(t, target)
	role := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{Id: role, OrgId: org, Name: "Example role " + role, Permissions: []*gen.Permission{{Resource: "solution", Action: "use"}}})
	}))
	installation, err := s.delivery.service.InstallSolution(testCtx, s.admin, &business.InstallSolutionParams{
		OrgID: org, TargetID: target, AgentIdentifier: "acme.example/registry:1.0.0", RoleID: role,
	})
	require.NoError(t, err)
	acknowledgeInstallation(t, installation)
	return installation
}

// TestDeliveringAManifestChangesTheRegistryWithNoRestart is the acceptance row
// for an added module: the envelope grants it, its manifest asks for it, and
// the registry every capability decision reads holds the intersection — on a
// running service, within the pass the delivery triggered.
func TestDeliveringAManifestChangesTheRegistryWithNoRestart(t *testing.T) {
	s := newRegistryScenario(t, "reg")
	s.putEnvelope(t, envelopeOptions{resources: true})
	var requested atomic.Int32
	s.delivery.service.SetModuleRegistryPassTrigger(func() {
		requested.Add(1)
		s.reconciler.Trigger()
	})
	ctx, cancel := context.WithCancel(testCtx)
	t.Cleanup(func() {
		cancel()
		_ = s.reconciler.Shutdown(context.Background())
	})
	s.reconciler.Start(ctx)
	_, present := s.entry()
	require.False(t, present, "a ceiling alone grants nothing: no manifest has asked for this principal")

	before := requested.Load()
	s.deliverManifest(t, s.primary, 1)
	require.Equal(t, before+1, requested.Load(), "an accepted delivery asks for a pass rather than waiting for the tick")
	require.Eventually(t, func() bool { _, ok := s.entry(); return ok }, 20*time.Second, 50*time.Millisecond,
		"a delivered manifest inside the envelope must reach the registry without a restart or a tick")

	grant, _ := s.entry()
	require.Equal(t, s.prefix, grant.Prefix)
	require.Equal(t, s.tenant, grant.Tenant)
	require.Equal(t, s.primary.binding, grant.PresenceBinding)
	require.Equal(t, []business.ModuleWorkload{s.primary.workload()}, grant.Workloads,
		"the workloads are the applied manifest's, spelled by the cell's rule")
	require.Equal(t, []string{"reports.render"}, grant.Queues)
	require.Equal(t, []string{s.resource}, grant.Resources)
	require.Equal(t, []business.ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read", "write"}}},
		grant.OperationAudiences["run"].InvokeScopes)
	s.install(t, s.tenant)
	_, err := s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.NoError(t, err)
}

// Narrowing the envelope refuses the next check before any pass, and the pass
// that follows derives the narrower intersection.
func TestNarrowingTheEnvelopeRefusesTheNextCheck(t *testing.T) {
	s := newRegistryScenario(t, "nar")
	s.established(t)

	s.putEnvelope(t, envelopeOptions{resources: false})
	_, err := s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.ErrorIs(t, err, business.ErrModuleRegistryStale, "the next check after a narrowing is refused before any pass runs")

	s.pass(t)
	grant, present := s.entry()
	require.True(t, present)
	require.Empty(t, grant.Resources, "the grant no longer allows the resource kind the manifest asks for")
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.NoError(t, err)

	s.putEnvelope(t, envelopeOptions{none: true})
	require.ErrorIs(t, s.passReasons(t), business.ErrModulePrincipalOmitted)
	_, present = s.entry()
	require.False(t, present, "an envelope with no ceiling for the principal grants it nothing")
}

// A withdrawal takes the principal out and is terminal for the binding.
func TestAWithdrawalIsTerminalForTheBinding(t *testing.T) {
	s := newRegistryScenario(t, "wd")
	s.established(t)
	requireDelivered(t, s.delivery.deliver(t, productionPathWithdrawal(t, s.manifest(t, s.primary, 1), 2)))
	s.pass(t)
	_, present := s.entry()
	require.False(t, present, "a withdrawn binding's principal is out of the registry")
	_, err := s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, s.delivery.deliver(t, s.manifest(t, s.primary, 3)).Code,
		"no later generation brings a withdrawn binding back")
}

// A key unpinned since a manifest was applied keeps the binding applied — a
// removal is a withdrawal generation, never a lost pin — and grants nothing.
func TestAnUnpinnedSignerGrantsNothing(t *testing.T) {
	s := newRegistryScenario(t, "pin")
	s.established(t)
	other := deliverytest.Verifier(t, map[string][]string{"another-key": {productionPathDomain}})
	repinned := newRegistryReconciler(t, s.delivery.service, other, s.delivery.service)
	refusals := repinned.RunOnce(testCtx)
	require.Error(t, refusals)
	_, present := s.entry()
	require.False(t, present)
	require.NotNil(t, productionPathBindingRecord(t, s.delivery.service, s.primary.binding).Applied)
}

// THE MINT, BY RULE: a projected token for the manifest's workload — the
// module's namespace, the service's account — binds; any other pair, and the
// same pair once the binding is withdrawn, does not.
func TestTheMintIsBoundToTheManifestsWorkloadByRule(t *testing.T) {
	s := newRegistryScenario(t, "mint")
	s.established(t)
	cluster := executiontest.New()
	s.delivery.service.SetExecutionBinding(cluster)
	workload := s.primary.workload()
	declared := executiontest.Workload{Namespace: workload.Namespace, ServiceAccount: workload.ServiceAccount, Container: workload.Container}

	identity, err := s.delivery.service.ModuleAuthorizeWorkContext(testCtx, s.prefix, cluster.Run(declared))
	require.NoError(t, err)
	require.Equal(t, s.principalID(), identity.PrincipalID)
	require.Equal(t, workload.Namespace, identity.Execution.Namespace)

	for name, wrong := range map[string]executiontest.Workload{
		"the bare module namespace": {Namespace: s.primary.solutionID, ServiceAccount: "api", Container: "api"},
		"another account":           {Namespace: workload.Namespace, ServiceAccount: "intruder", Container: "api"},
		"another module's namespace": {
			Namespace: productionPathNamespace("billing"), ServiceAccount: "api", Container: "api",
		},
	} {
		_, err := s.delivery.service.ModuleAuthorizeWorkContext(testCtx, s.prefix, cluster.Run(wrong))
		require.ErrorIs(t, err, business.ErrModuleIdentityDenied, name)
	}

	requireDelivered(t, s.delivery.deliver(t, productionPathWithdrawal(t, s.manifest(t, s.primary, 1), 2)))
	s.pass(t)
	_, err = s.delivery.service.ModuleAuthorizeWorkContext(testCtx, s.prefix, cluster.Run(declared))
	require.ErrorIs(t, err, business.ErrModuleIdentityDenied, "a withdrawn binding's workload mints nothing")
}

// A manifest asking for no operation installs independently in two
// organisations: empty consumed scopes are still an installable principal.
func TestAManifestAskingForNothingInstallsIndependentlyInTwoOrganizations(t *testing.T) {
	s := newRegistryScenario(t, "empty")
	current := s.currentRevision(t)
	raw, err := json.Marshal(map[string]any{"revision": current + 1, "principals": map[string]any{
		s.prefix: map[string]any{"grant": map[string]any{"cross_tenant": true}},
	}})
	require.NoError(t, err)
	_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, raw)
	require.NoError(t, err)
	manifest := s.manifest(t, s.primary, 1)
	manifest.Authority = &solutionhost.Authority{Principal: s.prefix}
	requireDelivered(t, s.delivery.deliver(t, manifest))
	s.pass(t)
	grant, present := s.entry()
	require.True(t, present)
	require.Empty(t, grant.OperationAudiences)
	require.Empty(t, grant.Queues)
	require.Empty(t, grant.Resources)
	otherOrg := seedOrg(t, s.admin)
	require.NoError(t, testStore.As(business.Identity{OrgID: otherOrg}).AddOrgMember(testCtx, s.admin, "admin"))
	first := s.install(t, s.tenant)
	second := s.install(t, otherOrg)
	require.NotEqual(t, first.GetId(), second.GetId())
	for _, org := range []string{s.tenant, otherOrg} {
		_, err := s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(org))
		require.NoError(t, err)
	}
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(business.NewIDString()))
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
}
