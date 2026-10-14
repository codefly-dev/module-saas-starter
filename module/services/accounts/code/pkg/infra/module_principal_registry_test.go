//go:build !pure

package infra_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// THE MODULE REGISTRY, DERIVED FROM DELIVERY, through real Postgres.
//
// The defect these cover: every capability decision read MODULE_PRINCIPALS,
// parsed once from the environment at boot, and a delivered authority document
// reached nothing any decision read. Adding, narrowing or withdrawing a module
// was a configuration edit and a restart.
//
// Each test drives the host's own path: a platform super_admin writes the
// module envelope through the business method the RPC calls, presence and
// authority are POSTed through the real delivery handler, and the registry is
// read back through the service every capability decision reads — with no
// service rebuilt and nothing read from the environment between the steps.
//
// The envelope is HOST-WIDE and append-only, like the inbox: each test writes
// the next revision after whatever is current, and asserts only on its own
// principal. Other tests' documents sit in the same inbox and are refused as
// outside this test's ceiling, which is the shape of a real host.

// registryBinding is one presence binding of the scenario's module: one
// deployed instance, with its own namespace and workload.
type registryBinding struct {
	solutionID string
	namespace  string
	binding    string
	build      solutionhost.ImageDigest
}

func newRegistryBinding(t *testing.T, label string) *registryBinding {
	t.Helper()
	solutionID := uniqueAlias(label)
	namespace := "ns-" + solutionID
	presence := productionPathBinding(t, solutionID, namespace, 1)
	return &registryBinding{solutionID: solutionID, namespace: namespace, binding: presence.Binding, build: presence.Builds()[0]}
}

// spiffeID is the identity productionPathBinding's workload presents.
func (b *registryBinding) spiffeID() string {
	return "spiffe://acme.test/ns/" + b.namespace + "/sa/api"
}

func (b *registryBinding) unitID(operation string) string { return b.binding + ":run:" + operation }

// units are the units delivery writes for this binding: invoke and lookup of
// the `run` binding, field for field what the envelope admits.
func (b *registryBinding) units() []solutionhost.AuthorityBinding {
	return []solutionhost.AuthorityBinding{
		{ID: b.unitID("invoke"), Revision: 1, Audience: "example-producer", Scope: "profiles:read,profiles:write"},
		{ID: b.unitID("lookup"), Revision: 1, Audience: "example-producer", Scope: "profiles:read"},
	}
}

// registryScenario is one module on one host: its principal, its presence
// bindings, and the reconciler deriving the registry.
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
	scenario := &registryScenario{
		delivery: delivery,
		admin:    seedUser(t),
		tenant:   business.NewIDString(),
		primary:  newRegistryBinding(t, label),
	}
	seedHumanPrincipal(t, scenario.admin, "Jane Doe")
	scenario.tenant = seedOrg(t, scenario.admin)
	require.NoError(t, testStore.As(business.Identity{OrgID: scenario.tenant}).AddOrgMember(testCtx, scenario.admin, "admin"))
	scenario.prefix = "mod-" + scenario.primary.solutionID
	scenario.resource = scenario.prefix + ".reports"
	grantPlatformRoleForTest(t, scenario.admin, "super_admin")
	scenario.reconciler = newRegistryReconciler(t, delivery.service, productionVerifier(t),
		map[string][]string{productionPathSigner: {productionPathDomain}}, delivery.service)
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
	t *testing.T, service *business.Service, verifier *solutionhost.Verifier,
	domainsBySigner map[string][]string, envelopes business.ModuleEnvelopeSource,
) *business.SolutionHostBindingReconciler {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(service,
		business.SolutionHostBindingReconcilerConfig{
			Source:          business.NewDeliveredSolutionHostBindings(service),
			Verifier:        verifier,
			Coordinate:      productionPathCoordinate,
			Domains:         []string{productionPathDomain},
			DomainsBySigner: domainsBySigner,
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

// envelopeOptions is what one revision of the ceiling admits.
type envelopeOptions struct {
	crossTenant bool
	// resources lets the module's own content kind through the grant.
	resources bool
	// tenant overrides the scenario's tenant.
	tenant string
	// bindings are the presence bindings whose units are admitted, and whose
	// workloads are allowed.
	bindings []*registryBinding
	// none writes a ceiling with no entry for the scenario's principal.
	none bool
}

func (s *registryScenario) envelope(t *testing.T, revision uint64, options envelopeOptions) []byte {
	t.Helper()
	tenant := s.tenant
	if options.tenant != "" {
		tenant = options.tenant
	}
	if options.bindings == nil {
		options.bindings = []*registryBinding{s.primary}
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
	var units, workloads []any
	for _, binding := range options.bindings {
		for _, unit := range binding.units() {
			units = append(units, map[string]any{"id": unit.ID, "revision": unit.Revision, "scope": unit.Scope})
		}
		workloads = append(workloads, map[string]any{"spiffe_id": binding.spiffeID(), "container": "api"})
	}
	principals := map[string]any{}
	if !options.none {
		principals[s.prefix] = map[string]any{
			"bindings":            units,
			"workload_identities": workloads,
			"scope_ceilings":      []any{map[string]any{"resource_kind": s.resource, "actions": []string{"read"}}},
			"grant":               grant,
		}
	}
	builds := map[string]bool{}
	var approved []string
	for _, binding := range options.bindings {
		if !builds[string(binding.build)] {
			builds[string(binding.build)] = true
			approved = append(approved, string(binding.build))
		}
	}
	raw, err := json.Marshal(map[string]any{"revision": revision, "approved_builds": approved, "principals": principals})
	require.NoError(t, err)
	return raw
}

// putEnvelope writes the next revision as the super_admin, through the method
// PutModuleEnvelope calls, and returns it.
func (s *registryScenario) putEnvelope(t *testing.T, options envelopeOptions) uint64 {
	t.Helper()
	current := s.currentRevision(t)
	record, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, s.envelope(t, current+1, options))
	require.NoError(t, err)
	require.Equal(t, current+1, record.Revision)
	return record.Revision
}

func (s *registryScenario) deliverPresence(t *testing.T, binding *registryBinding, generation, revision uint64, mutate ...func(*solutionhost.SolutionHostBinding)) {
	t.Helper()
	presence := productionPathBinding(t, binding.solutionID, binding.namespace, generation)
	presence.EnvelopeRevision = revision
	for _, change := range mutate {
		change(presence)
	}
	require.NoError(t, presence.Validate())
	s.delivery.callerNamespace = binding.namespace
	requireDelivered(t, s.delivery.post(t, business.SolutionDeliveryPresence, productionPathCarrier(t, presence)))
}

// authority is an authority document over one binding: the module's name as
// the principal, the units delivery derives, at an envelope revision.
func (s *registryScenario) authority(
	t *testing.T, binding *registryBinding, generation, revision uint64, removed bool, principals map[string][]solutionhost.AuthorityBinding,
) *solutionhost.AuthorityDocument {
	t.Helper()
	document := &solutionhost.AuthorityDocument{
		Schema:           solutionhost.SchemaAuthorityV1,
		Authority:        binding.binding + "-authority",
		Generation:       generation,
		PresenceBinding:  binding.binding,
		Host:             solutionhost.HostTarget{Coordinate: productionPathCoordinate, Component: "saas-host"},
		OwnershipDomain:  productionPathDomain,
		EnvelopeRevision: revision,
		Queues:           []string{"reports.render"},
		Namespaces:       []string{"reports"},
		ScopeCeilings:    []solutionhost.ScopeCeiling{{ResourceKind: s.resource, Actions: []string{"read"}}},
	}
	if removed {
		document.Queues, document.Namespaces, document.ScopeCeilings = []string{}, []string{}, []solutionhost.ScopeCeiling{}
		document.Removed = true
	} else {
		document.ApprovedBuild = binding.build
		document.EffectiveFrom = 1
		for principal, units := range principals {
			document.Principals = append(document.Principals, solutionhost.PrincipalAuthority{Principal: principal, Bindings: units})
		}
	}
	require.NoError(t, document.Validate())
	return document
}

// grantsTo is the usual principal map: this module's own principal, holding
// the binding's units.
func (s *registryScenario) grantsTo(binding *registryBinding) map[string][]solutionhost.AuthorityBinding {
	return map[string][]solutionhost.AuthorityBinding{s.prefix: binding.units()}
}

func (s *registryScenario) deliverAuthority(t *testing.T, document *solutionhost.AuthorityDocument) {
	t.Helper()
	s.delivery.callerNamespace = authorityWriterNamespace
	requireDelivered(t, s.delivery.post(t, business.SolutionDeliveryAuthority, productionPathAuthorityCarrier(t, document)))
}

// established is the common start: an envelope admitting the primary binding,
// its presence applied, and its authority delivered and derived. It returns
// the envelope revision.
func (s *registryScenario) established(t *testing.T) uint64 {
	t.Helper()
	revision := s.putEnvelope(t, envelopeOptions{resources: true})
	s.deliverPresence(t, s.primary, 1, revision)
	s.pass(t)
	s.deliverAuthority(t, s.authority(t, s.primary, 1, revision, false, s.grantsTo(s.primary)))
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
		require.NotContains(t, err.Error(), s.prefix, "the pass refused this scenario's own principal")
	}
}

// TestDeliveringAuthorityChangesTheRegistryWithNoRestart is the acceptance row
// for an added module: the envelope allows it, delivery asks for it, and the
// registry every capability decision reads holds it — on a running service,
// within the pass the delivery triggered.
func TestDeliveringAuthorityChangesTheRegistryWithNoRestart(t *testing.T) {
	s := newRegistryScenario(t, "reg")
	revision := s.putEnvelope(t, envelopeOptions{resources: true})
	s.deliverPresence(t, s.primary, 1, revision)

	// The pass trigger is wired as work.go wires it, through a counter: the
	// assertion that the DELIVERY asked for the pass is what makes this test
	// about the trigger rather than about whichever pass happened to run.
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
	require.Eventually(t, func() bool {
		records, err := s.delivery.service.ListSolutionHostBindings(testCtx)
		if err != nil {
			return false
		}
		for _, one := range records {
			if one.BindingID == s.primary.binding && one.Applied != nil {
				return true
			}
		}
		return false
	}, 20*time.Second, 50*time.Millisecond, "the presence was never applied")
	_, present := s.entry()
	require.False(t, present, "a ceiling alone grants nothing: no delivery has asked for this principal")

	before := requested.Load()
	s.deliverAuthority(t, s.authority(t, s.primary, 1, revision, false, s.grantsTo(s.primary)))
	require.Equal(t, before+1, requested.Load(), "an accepted delivery asks for a pass rather than waiting for the tick")
	require.Eventually(t, func() bool { _, ok := s.entry(); return ok }, 20*time.Second, 50*time.Millisecond,
		"a delivered authority inside the envelope must reach the registry without a restart or a tick")

	grant, _ := s.entry()
	require.Equal(t, s.prefix, grant.Prefix, "the principal is the module's own name")
	require.Equal(t, s.tenant, grant.Tenant)
	require.Equal(t, business.ModuleWorkload{SPIFFEID: s.primary.spiffeID(), ServiceAccount: "api", Namespace: s.primary.namespace, Container: "api"}, grant.Workload,
		"the workload is the applied presence's, and one the envelope allows")
	require.Equal(t, []string{"reports.render"}, grant.Queues)
	require.Equal(t, []string{s.resource}, grant.Resources)
	require.Equal(t, []business.ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read", "write"}}},
		grant.OperationAudiences["run"].InvokeScopes)

	resources, err := s.delivery.service.ModuleContentResources(s.prefix)
	require.NoError(t, err, "the capability decision reads the derived registry")
	require.Equal(t, []string{s.resource}, resources)
	s.install(t, s.tenant)
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.NoError(t, err)
}

// TestNarrowingTheEnvelopeRefusesTheNextCheck (finding 4): the check right
// after the write is refused — no pass run, no trigger consumed — and refused
// as a registry older than the committed ceiling. The pass that follows
// derives the narrower registry: by revision first, then through the
// intersection once delivery renders against the narrower ceiling.
func TestNarrowingTheEnvelopeRefusesTheNextCheck(t *testing.T) {
	s := newRegistryScenario(t, "nar")
	s.established(t)

	narrow := s.putEnvelope(t, envelopeOptions{resources: false})
	_, err := s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.ErrorIs(t, err, business.ErrModuleRegistryStale, "the next check after a narrowing is refused before any pass runs")
	_, err = s.delivery.service.ModuleContentResources(s.prefix)
	require.Error(t, err, "and so is every read of the registry the narrowing predates")

	refusals := s.passReasons(t)
	_, present := s.entry()
	require.False(t, present, "a document rendered against the previous revision is outside the new one")
	require.ErrorIs(t, refusals, business.ErrModuleAuthorityOutsideEnvelope)

	// REDELIVERED at the narrower revision: inside it, and bounded by it.
	s.deliverPresence(t, s.primary, 2, narrow)
	s.pass(t)
	s.deliverAuthority(t, s.authority(t, s.primary, 2, narrow, false, s.grantsTo(s.primary)))
	s.pass(t)
	grant, present := s.entry()
	require.True(t, present, "the redelivered document is inside the narrower ceiling")
	require.Empty(t, grant.Resources, "the module asks for its content and the envelope no longer allows it")
	_, err = s.delivery.service.ModuleContentResources(s.prefix)
	require.Error(t, err, "the narrowing holds through the intersection, not only through the revision")
}

// TestANarrowingOnOneReplicaRefusesTheNextCheckOnAnother (finding 4): the
// replica that did not take the write receives no trigger and runs no pass,
// and still refuses at its next check, because the fence reads the committed
// revision from the store.
func TestANarrowingOnOneReplicaRefusesTheNextCheckOnAnother(t *testing.T) {
	s := newRegistryScenario(t, "rep")
	s.established(t)

	other := newRegistryDelivery(t)
	otherReconciler := newRegistryReconciler(t, other.service, productionVerifier(t),
		map[string][]string{productionPathSigner: {productionPathDomain}}, other.service)
	if err := otherReconciler.RunOnce(testCtx); err != nil {
		require.NotErrorIs(t, err, business.ErrModulePrincipalInputsUnreadable)
	}
	_, present := entryOn(other.service, s.prefix)
	require.True(t, present, "the second replica derived the same registry")
	_, err := other.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.NoError(t, err)

	s.putEnvelope(t, envelopeOptions{none: true})
	_, err = other.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.ErrorIs(t, err, business.ErrModuleRegistryStale,
		"a replica that has not run a pass since the narrowing refuses at its next check")
	_, present = entryOn(other.service, s.prefix)
	require.False(t, present, "and once it has seen the committed revision it serves nothing from the older registry")
}

// staleCeiling returns one fixed envelope, the way a pass that read the
// previous revision just before a write committed would see it.
type staleCeiling struct{ envelope *business.ModuleEnvelope }

func (c staleCeiling) CurrentModuleEnvelope(context.Context) (*business.ModuleEnvelope, error) {
	return c.envelope, nil
}

// TestAPassThatReadThePreviousCeilingDoesNotServeAfterANarrowing (finding 4):
// the in-flight race. A pass reads revision N, the narrowing N+1 commits, the
// pass publishes its registry tagged N — and the next check refuses it.
func TestAPassThatReadThePreviousCeilingDoesNotServeAfterANarrowing(t *testing.T) {
	s := newRegistryScenario(t, "inf")
	s.established(t)
	previous, err := s.delivery.service.CurrentModuleEnvelope(testCtx)
	require.NoError(t, err)

	s.putEnvelope(t, envelopeOptions{none: true})
	inFlight := newRegistryReconciler(t, s.delivery.service, productionVerifier(t),
		map[string][]string{productionPathSigner: {productionPathDomain}}, staleCeiling{envelope: previous})
	_ = inFlight.ModulePrincipals().RunOnce(testCtx)

	_, present := s.entry()
	require.False(t, present, "a registry derived from the revision a committed narrowing replaced serves nothing")
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.ErrorIs(t, err, business.ErrModuleRegistryStale)
}

// TestANarrowingHoldsWhileTheInboxIsUnreadable (finding 4): the envelope is
// narrowed and the documents no longer verify. The registry is derived from
// the NEW ceiling — the narrowing takes effect — rather than the previous
// registry being kept.
func TestANarrowingHoldsWhileTheInboxIsUnreadable(t *testing.T) {
	s := newRegistryScenario(t, "unb")
	verifier := productionVerifier(t)
	s.reconciler = newRegistryReconciler(t, s.delivery.service, verifier,
		map[string][]string{productionPathSigner: {productionPathDomain}}, s.delivery.service)
	s.established(t)

	s.putEnvelope(t, envelopeOptions{none: true})
	*verifier = solutionhost.Verifier{}
	err := s.reconciler.ModulePrincipals().RunOnce(testCtx)
	require.ErrorIs(t, err, business.ErrSolutionAuthorityUnverifiable, "a document that no longer verifies is refused by name")
	_, present := s.entry()
	require.False(t, present, "a read failure never keeps a grant the committed ceiling withdrew")
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.Equal(t, codes.PermissionDenied, status.Code(err),
		"the registry was DERIVED from the new ceiling — the principal is simply not in it — rather than the old one kept and fenced")
	require.NotErrorIs(t, err, business.ErrModuleRegistryStale)
}

// TestAWithdrawalIsTerminalForTheBinding (finding 2): authority X withdraws
// the binding at generation 2, then a live generation 3 of the SAME id is
// delivered. The newest-per-id read no longer contains the tombstone; the
// history does, and the binding stays withdrawn — in the registry, in
// activation, and for a reconciler built after a restart.
func TestAWithdrawalIsTerminalForTheBinding(t *testing.T) {
	s := newRegistryScenario(t, "wdr")
	revision := s.established(t)

	s.deliverAuthority(t, s.authority(t, s.primary, 2, revision, true, nil))
	s.pass(t)
	_, present := s.entry()
	require.False(t, present, "a withdrawn authority leaves its principal out of the registry")

	s.deliverAuthority(t, s.authority(t, s.primary, 3, revision, false, s.grantsTo(s.primary)))
	refusals := s.passReasons(t)
	_, present = s.entry()
	require.False(t, present, "a later generation of the same authority does not reinstate a withdrawn binding")
	require.ErrorIs(t, refusals, business.ErrSolutionAuthorityWithdrawn)

	_, err := s.reconciler.AuthorityActivation().Activate(testCtx, s.primary.binding, s.primary.build)
	require.ErrorIs(t, err, business.ErrSolutionAuthorityWithdrawn, "activation reads the same history")

	restarted := newRegistryReconciler(t, s.delivery.service, productionVerifier(t),
		map[string][]string{productionPathSigner: {productionPathDomain}}, s.delivery.service)
	_ = restarted.ModulePrincipals().RunOnce(testCtx)
	_, present = s.entry()
	require.False(t, present, "the withdrawal is read from the store, so a restart does not forget it")
}

// TestAPrincipalMovesToAnotherAuthorityOnlyInsideTheEnvelope (finding 2): X
// stops naming the principal and Y, over another presence binding, names it at
// a lower generation. Generations of two ids are not comparable; what decides
// is whether the envelope admits Y's units — the administrator's approval of
// the move — and the answer is the same after a restart.
func TestAPrincipalMovesToAnotherAuthorityOnlyInsideTheEnvelope(t *testing.T) {
	s := newRegistryScenario(t, "mov")
	second := newRegistryBinding(t, "mov2")
	other := "oth-" + s.primary.solutionID

	// Revision R1 admits the primary binding only.
	r1 := s.putEnvelope(t, envelopeOptions{resources: true})
	s.deliverPresence(t, s.primary, 1, r1)
	s.deliverPresence(t, second, 1, r1)
	s.pass(t)
	for generation := uint64(1); generation <= 5; generation++ {
		s.deliverAuthority(t, s.authority(t, s.primary, generation, r1, false, s.grantsTo(s.primary)))
	}
	s.pass(t)
	grant, present := s.entry()
	require.True(t, present)
	require.Equal(t, s.primary.namespace, grant.Workload.Namespace)

	// X moves on to another principal; Y names this one at generation 1.
	s.deliverAuthority(t, s.authority(t, s.primary, 6, r1, false,
		map[string][]solutionhost.AuthorityBinding{other: s.primary.units()}))
	s.deliverAuthority(t, s.authority(t, second, 1, r1, false, s.grantsTo(second)))
	require.ErrorIs(t, s.passReasons(t), business.ErrModuleAuthorityOutsideEnvelope)
	_, present = s.entry()
	require.False(t, present, "Y's units are not admitted, so the move is not approved")

	// Revision R2 admits both bindings: the move is approved.
	r2 := s.putEnvelope(t, envelopeOptions{resources: true, bindings: []*registryBinding{s.primary, second}})
	s.deliverPresence(t, s.primary, 2, r2)
	s.deliverPresence(t, second, 2, r2)
	_ = s.passReasons(t) // applies the presence; the r1 documents are outside r2
	s.deliverAuthority(t, s.authority(t, s.primary, 7, r2, false,
		map[string][]solutionhost.AuthorityBinding{other: s.primary.units()}))
	s.deliverAuthority(t, s.authority(t, second, 2, r2, false, s.grantsTo(second)))
	// The registry step alone: the approved-build view keeps its own
	// process-local ordering across ids (it feeds BindExecution, which no mint
	// calls yet) and refuses this move until a restart; the registry does not.
	if err := s.reconciler.ModulePrincipals().RunOnce(testCtx); err != nil {
		require.NotContains(t, err.Error(), s.prefix, "the registry refused the approved move")
	}
	grant, present = s.entry()
	require.True(t, present, "inside the envelope, the replacement authority holds the principal")
	require.Equal(t, second.namespace, grant.Workload.Namespace, "and binds it to the replacement's approved workload")

	restarted := newRegistryReconciler(t, s.delivery.service, productionVerifier(t),
		map[string][]string{productionPathSigner: {productionPathDomain}}, s.delivery.service)
	_ = restarted.ModulePrincipals().RunOnce(testCtx)
	again, present := s.entry()
	require.True(t, present, "the answer does not depend on whether the process restarted")
	require.Equal(t, grant, again)
}

// TestARevokedSignerDomainGrantsNothing (finding 5): the signer's permission to
// speak for the domain is removed from the host's policy. Its stored documents
// still verify, and grant nothing.
func TestARevokedSignerDomainGrantsNothing(t *testing.T) {
	s := newRegistryScenario(t, "sig")
	s.established(t)

	narrowed := newRegistryReconciler(t, s.delivery.service, productionVerifier(t),
		map[string][]string{productionPathSigner: {solutionhost.FixtureDomain}}, s.delivery.service)
	refusals := narrowed.ModulePrincipals().RunOnce(testCtx)
	_, present := s.entry()
	require.False(t, present, "a signer that may no longer speak for the domain authorizes nothing in it")
	require.ErrorIs(t, refusals, business.ErrSolutionAuthoritySignerNotGranted)
}

// TestAPresenceGenerationCannotRetargetThePrincipal (finding 6): a later
// presence generation runs the approved build under another service account.
// It applies — presence is presence — and the principal does not follow it.
func TestAPresenceGenerationCannotRetargetThePrincipal(t *testing.T) {
	s := newRegistryScenario(t, "ret")
	revision := s.established(t)

	s.deliverPresence(t, s.primary, 2, revision, func(presence *solutionhost.SolutionHostBinding) {
		presence.Workloads[0].Identity.Subject = "system:serviceaccount:" + s.primary.namespace + ":intruder"
		presence.Workloads[0].Identity.SPIFFEID = "spiffe://acme.test/ns/" + s.primary.namespace + "/sa/intruder"
	})
	refusals := s.passReasons(t)
	_, present := s.entry()
	require.False(t, present, "the principal binds to the workload the envelope allows, not to whatever presence names next")
	require.ErrorIs(t, refusals, business.ErrModuleWorkloadNotApproved)
}

// TestAWidenedUnitIsRefusedAndActivatesNothing (finding 7): the document keeps
// an admitted unit id and widens its scope. The ceiling core compares against
// is the envelope's own unit, so the document is outside it — in the registry
// and in activation.
func TestAWidenedUnitIsRefusedAndActivatesNothing(t *testing.T) {
	s := newRegistryScenario(t, "wid")
	revision := s.putEnvelope(t, envelopeOptions{resources: true})
	s.deliverPresence(t, s.primary, 1, revision)
	s.pass(t)

	units := s.primary.units()
	units[1].Scope = "profiles:delete,profiles:read"
	document := s.authority(t, s.primary, 1, revision, false, map[string][]solutionhost.AuthorityBinding{s.prefix: units})
	s.deliverAuthority(t, document)
	refusals := s.passReasons(t)
	_, present := s.entry()
	require.False(t, present)
	require.ErrorIs(t, refusals, business.ErrModuleAuthorityOutsideEnvelope)
	require.Contains(t, refusals.Error(), document.Authority, "the refusal names the document")

	_, err := s.reconciler.AuthorityActivation().Activate(testCtx, s.primary.binding, s.primary.build)
	require.ErrorIs(t, err, solutionhost.ErrOutsideEnvelope, "activation holds the document to the same envelope-built units")
}

// TestMovingThePrincipalsTenantRevokesTheOldTenantsCredentials (finding 1): the
// envelope moves the principal to another tenant. A credential sealed for the
// old tenant is refused at the next check, named as such; one for the new
// tenant is accepted.
func TestMovingThePrincipalsTenantRevokesTheOldTenantsCredentials(t *testing.T) {
	s := newRegistryScenario(t, "ten")
	s.established(t)
	moved := seedOrg(t, s.admin)
	require.NoError(t, testStore.As(business.Identity{OrgID: moved}).AddOrgMember(testCtx, s.admin, "admin"))

	revision := s.putEnvelope(t, envelopeOptions{resources: true, tenant: moved})
	s.deliverPresence(t, s.primary, 2, revision)
	s.pass(t)
	s.deliverAuthority(t, s.authority(t, s.primary, 2, revision, false, s.grantsTo(s.primary)))
	s.pass(t)

	_, err := s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.ErrorIs(t, err, business.ErrModuleTenantNotCurrent, "the old tenant's credential is refused, and named as such")
	_, err = testStore.LiveModuleAuthority(testCtx, business.ModulePrincipalID(s.prefix), s.tenant, "")
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive, "tenancy revoked the previous consent in the envelope transaction")
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(moved))
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive, "moving tenancy creates no consent")
	s.install(t, moved)
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(moved))
	require.NoError(t, err)
}

// TestADocumentOutsideTheEnvelopeGrantsNothing: signed and delivered is not
// allowed. A unit the envelope does not admit puts the whole document outside
// the ceiling, so the principal is absent and the refusal names the document.
func TestADocumentOutsideTheEnvelopeGrantsNothing(t *testing.T) {
	s := newRegistryScenario(t, "out")
	revision := s.putEnvelope(t, envelopeOptions{resources: true})
	s.deliverPresence(t, s.primary, 1, revision)
	s.pass(t)

	units := append(s.primary.units(), solutionhost.AuthorityBinding{
		ID: s.primary.binding + ":admin:invoke", Revision: 1, Audience: "example-producer", Scope: "profiles:delete",
	})
	document := s.authority(t, s.primary, 1, revision, false, map[string][]solutionhost.AuthorityBinding{s.prefix: units})
	s.deliverAuthority(t, document)
	refusals := s.passReasons(t)

	_, present := s.entry()
	require.False(t, present, "a document claiming one unit more than the envelope admits grants nothing at all")
	require.ErrorIs(t, refusals, business.ErrModuleAuthorityOutsideEnvelope)
	require.Contains(t, refusals.Error(), document.Authority, "the refusal names the document, so an operator can act on it")
}

// TestAnUnreadableEnvelopeServesNothingItPredates: a stored revision this
// binary no longer accepts is not "no ceiling", and it is not permission to
// keep serving the revision before it either. Nothing is derived, and the
// fence refuses the older registry; the next valid revision is applied as
// usual.
func TestAnUnreadableEnvelopeServesNothingItPredates(t *testing.T) {
	s := newRegistryScenario(t, "unr")
	revision := s.established(t)

	// A revision written past the validation PutModuleEnvelope applies — the
	// shape a row written by an older binary would have.
	broken := []byte(`{"revision":` + jsonNumber(revision+1) + `,"principals":{"` + s.prefix + `":{"workload":{}}}}`)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.AppendModuleEnvelopeRecord(ctx, &business.ModuleEnvelopeRecord{
			Revision: revision + 1, Document: broken, WrittenBy: s.admin,
		}, revision)
	}))
	err := s.reconciler.RunOnce(testCtx)
	require.ErrorIs(t, err, business.ErrModulePrincipalInputsUnreadable)
	_, err = s.delivery.service.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.ErrorIs(t, err, business.ErrModuleRegistryStale, "a committed revision nobody can read still fences the one before it")

	s.putEnvelope(t, envelopeOptions{resources: true})
	s.pass(t)
	_, present := s.entry()
	require.False(t, present, "the document rendered against the old revision is outside the new one")
}

// TestTheEnvelopeIsWrittenOnlyAtTheNextRevision: a writer that read a stale
// revision is refused and writes nothing; a document carrying another revision
// than the one it replaces is refused; a caller who is not a super_admin is
// refused before anything is parsed.
func TestTheEnvelopeIsWrittenOnlyAtTheNextRevision(t *testing.T) {
	s := newRegistryScenario(t, "rev")
	current := s.currentRevision(t)
	_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, s.envelope(t, current+1, envelopeOptions{resources: true}))
	require.NoError(t, err)

	_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, s.envelope(t, current+1, envelopeOptions{}))
	var storeErr *business.StoreError
	require.True(t, errors.As(err, &storeErr))
	require.Equal(t, business.ErrTypeConflict, storeErr.StoreErrorType, "a stale expected revision is a conflict, never a second write")

	_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current+1, s.envelope(t, current+3, envelopeOptions{resources: true}))
	require.True(t, errors.As(err, &storeErr))
	require.Equal(t, business.ErrTypeValidation, storeErr.StoreErrorType)

	outsider := seedUser(t)
	_, err = s.delivery.service.PutModuleEnvelope(testCtx, outsider, current+1, s.envelope(t, current+2, envelopeOptions{resources: true}))
	require.True(t, errors.As(err, &storeErr))
	require.Equal(t, business.ErrTypePermission, storeErr.StoreErrorType)
	_, err = s.delivery.service.GetModuleEnvelope(testCtx, outsider)
	require.True(t, errors.As(err, &storeErr))
	require.Equal(t, business.ErrTypePermission, storeErr.StoreErrorType)

	record, err := s.delivery.service.GetModuleEnvelope(testCtx, s.admin)
	require.NoError(t, err)
	require.Equal(t, current+1, record.Revision, "none of the refused writes was stored")
	require.Equal(t, s.admin, record.WrittenBy)
	require.True(t, strings.Contains(string(record.Document), s.resource))
}

func jsonNumber(value uint64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// Registry tests explicitly install only when they exercise capability use.
// Delivery and reconciliation themselves never synthesize consent.
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

// This scenario starts at signed delivery and the persisted operator envelope,
// not a seeded principal registry. Empty consumed scopes still yield distinct
// organisation installations on one running host service.
func TestEmptyPermissionDeliveryInstallsIndependentlyInTwoOrganizations(t *testing.T) {
	s := newRegistryScenario(t, "empty")
	current := s.currentRevision(t)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(s.envelope(t, current+1, envelopeOptions{}), &wire))
	principal := wire["principals"].(map[string]any)[s.prefix].(map[string]any)
	principal["bindings"] = []any{}
	principal["admitted_bindings"] = []string{s.primary.binding}
	principal["scope_ceilings"] = []any{}
	principal["grant"] = map[string]any{"cross_tenant": true}
	raw, err := json.Marshal(wire)
	require.NoError(t, err)
	record, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, raw)
	require.NoError(t, err)
	s.deliverPresence(t, s.primary, 1, record.Revision)
	s.pass(t)
	document := s.authority(t, s.primary, 1, record.Revision, false, map[string][]solutionhost.AuthorityBinding{s.prefix: {}})
	document.Queues, document.Namespaces, document.ScopeCeilings = []string{}, []string{}, []solutionhost.ScopeCeiling{}
	s.deliverAuthority(t, document)
	s.pass(t)
	grant, present := s.entry()
	require.True(t, present)
	require.Empty(t, grant.OperationAudiences)
	require.Empty(t, grant.ReadAudiences)
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
