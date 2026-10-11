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

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
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
// read back from the service every capability decision reads — with no service
// rebuilt and nothing read from the environment between the steps.
//
// The envelope is HOST-WIDE and append-only, like the inbox: each test writes
// the next revision after whatever is current, and asserts only on its own
// principal. Other tests' documents sit in the same inbox and are refused as
// outside this test's ceiling, which is the shape of a real host.

// registryScenario is one module on one host: its presence binding, the
// principal its authority grants to, and the reconciler deriving the registry.
type registryScenario struct {
	delivery   *productionPathDelivery
	reconciler *business.SolutionHostBindingReconciler
	admin      string
	solutionID string
	namespace  string
	binding    string
	prefix     string
	tenant     string
	resource   string
	build      solutionhost.ImageDigest
}

func newRegistryScenario(t *testing.T, label string) *registryScenario {
	t.Helper()
	delivery := newProductionPathDelivery(t)
	delivery.service.SetModuleEnvelopeStore(testStore)
	solutionID := uniqueAlias(label)
	scenario := &registryScenario{
		delivery:   delivery,
		admin:      seedUser(t),
		solutionID: solutionID,
		namespace:  "ns-" + solutionID,
		prefix:     "mod-" + solutionID,
		tenant:     business.NewIDString(),
		resource:   "mod-" + solutionID + ".reports",
	}
	grantPlatformRoleForTest(t, scenario.admin, "super_admin")
	presence := productionPathBinding(t, solutionID, scenario.namespace, 1)
	scenario.binding = presence.Binding
	scenario.build = presence.Builds()[0]
	reconciler, err := business.NewSolutionHostBindingReconciler(delivery.service,
		business.SolutionHostBindingReconcilerConfig{
			Source:          business.NewDeliveredSolutionHostBindings(delivery.service),
			Verifier:        productionPathVerifier{signer: productionPathSigner},
			Coordinate:      productionPathCoordinate,
			Domains:         []string{productionPathDomain},
			DomainsBySigner: map[string][]string{productionPathSigner: {productionPathDomain}},
			// An hour: a pass these tests observe is one they ran or one a
			// delivery triggered, never one the clock happened to deliver.
			Interval:  time.Hour,
			Envelopes: delivery.service,
		})
	require.NoError(t, err)
	scenario.reconciler = reconciler
	return scenario
}

func (s *registryScenario) principalID() string { return business.ModulePrincipalID(s.prefix) }

func (s *registryScenario) entry() (business.ModulePrincipalGrant, bool) {
	grant, ok := s.delivery.service.ModulePrincipals()[s.principalID()]
	return grant, ok
}

func (s *registryScenario) unit(operation string) string {
	return s.binding + ":run:" + operation
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

// envelope is this module's ceiling at a revision: the run binding's invoke
// and lookup units, read and write on profiles, and the module's own resource
// kind as its content when resources is true.
func (s *registryScenario) envelope(t *testing.T, revision uint64, resources bool) []byte {
	t.Helper()
	grant := map[string]any{
		"tenant":     s.tenant,
		"queues":     []string{"reports.render"},
		"namespaces": []string{"reports"},
		"operation_audiences": map[string]any{
			"run": map[string]any{
				"audience": "example-producer",
				"invoke_scopes": []any{
					map[string]any{"resource_kind": "profiles", "actions": []string{"read", "write"}},
				},
				"lookup_scopes": []any{
					map[string]any{"resource_kind": "profiles", "actions": []string{"read"}},
				},
			},
		},
	}
	if resources {
		grant["resources"] = []string{s.resource}
	}
	document := map[string]any{
		"revision":        revision,
		"approved_builds": []string{string(s.build)},
		"principals": map[string]any{
			s.prefix: map[string]any{
				"bindings":       []string{s.unit("invoke"), s.unit("lookup")},
				"scope_ceilings": []any{map[string]any{"resource_kind": s.resource, "actions": []string{"read"}}},
				"grant":          grant,
			},
		},
	}
	raw, err := json.Marshal(document)
	require.NoError(t, err)
	return raw
}

// putEnvelope writes the next revision as the super_admin, through the method
// PutModuleEnvelope calls, and returns it.
func (s *registryScenario) putEnvelope(t *testing.T, resources bool) uint64 {
	t.Helper()
	current := s.currentRevision(t)
	record, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, s.envelope(t, current+1, resources))
	require.NoError(t, err)
	require.Equal(t, current+1, record.Revision)
	return record.Revision
}

func (s *registryScenario) deliverPresence(t *testing.T, generation, revision uint64) {
	t.Helper()
	presence := productionPathBinding(t, s.solutionID, s.namespace, generation)
	presence.EnvelopeRevision = revision
	require.NoError(t, presence.Validate())
	s.delivery.callerNamespace = s.namespace
	requireDelivered(t, s.delivery.post(t, business.SolutionDeliveryPresence, productionPathCarrier(t, presence)))
}

// authority is this module's authority document: the module's name as the
// principal, one unit per (binding, operation) as delivery derives them.
func (s *registryScenario) authority(
	t *testing.T, generation, revision uint64, removed bool, units ...solutionhost.AuthorityBinding,
) *solutionhost.AuthorityDocument {
	t.Helper()
	document := &solutionhost.AuthorityDocument{
		Schema:           solutionhost.SchemaAuthorityV1,
		Authority:        s.binding + "-authority",
		Generation:       generation,
		PresenceBinding:  s.binding,
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
		document.ApprovedBuild = s.build
		document.EffectiveFrom = 1
		document.Principals = []solutionhost.PrincipalAuthority{{Principal: s.prefix, Bindings: units}}
	}
	require.NoError(t, document.Validate())
	return document
}

func (s *registryScenario) units(invokeScope string) []solutionhost.AuthorityBinding {
	return []solutionhost.AuthorityBinding{
		{ID: s.unit("invoke"), Revision: 1, Audience: "example-producer", Scope: invokeScope},
		{ID: s.unit("lookup"), Revision: 1, Audience: "example-producer", Scope: "profiles:read"},
	}
}

func (s *registryScenario) deliverAuthority(t *testing.T, document *solutionhost.AuthorityDocument) {
	t.Helper()
	s.delivery.callerNamespace = authorityWriterNamespace
	requireDelivered(t, s.delivery.post(t, business.SolutionDeliveryAuthority, productionPathAuthorityCarrier(t, document)))
}

// passReasons runs one reconcile pass and returns its refusals. Refusals naming
// other tests' principals are expected — the inbox is host-wide — so it never
// requires a clean pass, only that the inputs were readable.
func (s *registryScenario) passReasons(t *testing.T) error {
	t.Helper()
	err := s.reconciler.RunOnce(testCtx)
	require.NotErrorIs(t, err, business.ErrModulePrincipalInputsUnreadable)
	return err
}

// pass runs one reconcile pass and requires that none of its refusals is this
// scenario's own principal.
func (s *registryScenario) pass(t *testing.T) {
	t.Helper()
	if err := s.passReasons(t); err != nil {
		require.NotContains(t, err.Error(), s.prefix, "the pass refused this scenario's own principal")
	}
}

// TestDeliveringAuthorityChangesTheRegistryWithNoRestart is the acceptance row
// for an added module: the envelope allows it, delivery asks for it, and the
// registry every capability decision reads holds it — on a running service,
// within the pass the delivery triggered.
func TestDeliveringAuthorityChangesTheRegistryWithNoRestart(t *testing.T) {
	s := newRegistryScenario(t, "reg")
	revision := s.putEnvelope(t, true)
	s.deliverPresence(t, 1, revision)

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
		record, err := s.delivery.service.ListSolutionHostBindings(testCtx)
		if err != nil {
			return false
		}
		for _, one := range record {
			if one.BindingID == s.binding && one.Applied != nil {
				return true
			}
		}
		return false
	}, 20*time.Second, 50*time.Millisecond, "the presence was never applied")
	_, present := s.entry()
	require.False(t, present, "a ceiling alone grants nothing: no delivery has asked for this principal")
	_, err := s.delivery.service.ModuleContentResources(s.prefix)
	require.Error(t, err)

	// THE DELIVERY. The loop ticks hourly, so the pass that installs the
	// principal is the one this POST triggered.
	before := requested.Load()
	s.deliverAuthority(t, s.authority(t, 1, revision, false, s.units("profiles:read,profiles:write")...))
	require.Equal(t, before+1, requested.Load(), "an accepted delivery asks for a pass rather than waiting for the tick")
	require.Eventually(t, func() bool { _, ok := s.entry(); return ok }, 20*time.Second, 50*time.Millisecond,
		"a delivered authority inside the envelope must reach the registry without a restart or a tick")

	grant, _ := s.entry()
	require.Equal(t, s.prefix, grant.Prefix, "the principal is the module's own name")
	require.Equal(t, s.tenant, grant.Tenant)
	require.Equal(t, business.ModuleWorkload{ServiceAccount: "api", Namespace: s.namespace, Container: "api"}, grant.Workload,
		"the workload is the applied presence's: namespace and service account from the SPIFFE ID, container from the workload")
	require.Equal(t, []string{"reports.render"}, grant.Queues)
	require.Equal(t, []string{s.resource}, grant.Resources)
	require.Equal(t, []business.ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read", "write"}}},
		grant.OperationAudiences["run"].InvokeScopes)

	resources, err := s.delivery.service.ModuleContentResources(s.prefix)
	require.NoError(t, err, "the capability decision reads the derived registry")
	require.Equal(t, []string{s.resource}, resources)
}

// TestNarrowingTheEnvelopeRefusesTheNextCheck: the ceiling is written at
// runtime and the next pass applies it — first by revision (a document is
// inside only the revision it was rendered against), then by intersection (a
// document redelivered at the narrower revision keeps only what it allows).
func TestNarrowingTheEnvelopeRefusesTheNextCheck(t *testing.T) {
	s := newRegistryScenario(t, "nar")
	wide := s.putEnvelope(t, true)
	s.deliverPresence(t, 1, wide)
	s.pass(t) // apply the presence
	s.deliverAuthority(t, s.authority(t, 1, wide, false, s.units("profiles:read,profiles:write")...))
	s.pass(t)
	_, err := s.delivery.service.ModuleContentResources(s.prefix)
	require.NoError(t, err, "the module serves under the wide ceiling")

	// NARROW: the module's content is no longer allowed.
	narrow := s.putEnvelope(t, false)
	refusals := s.passReasons(t)
	_, err = s.delivery.service.ModuleContentResources(s.prefix)
	require.Error(t, err, "the next check after a narrowing is refused, with no restart")
	_, present := s.entry()
	require.False(t, present, "a document rendered against the previous revision is outside the new one")
	require.ErrorIs(t, refusals, business.ErrModuleAuthorityOutsideEnvelope)

	// REDELIVERED at the narrower revision: inside it, and bounded by it.
	s.deliverPresence(t, 2, narrow)
	s.pass(t)
	s.deliverAuthority(t, s.authority(t, 2, narrow, false, s.units("profiles:read,profiles:write")...))
	s.pass(t)
	grant, present := s.entry()
	require.True(t, present, "the redelivered document is inside the narrower ceiling")
	require.Empty(t, grant.Resources, "the module asks for its content and the envelope no longer allows it")
	_, err = s.delivery.service.ModuleContentResources(s.prefix)
	require.Error(t, err, "the narrowing holds through the intersection, not only through the revision")
}

// TestADocumentOutsideTheEnvelopeGrantsNothing: signed and delivered is not
// allowed. A unit the envelope does not admit puts the whole document outside
// the ceiling, so the principal is absent and the refusal names the document.
func TestADocumentOutsideTheEnvelopeGrantsNothing(t *testing.T) {
	s := newRegistryScenario(t, "out")
	revision := s.putEnvelope(t, true)
	s.deliverPresence(t, 1, revision)
	s.pass(t)

	units := append(s.units("profiles:read"), solutionhost.AuthorityBinding{
		ID: s.binding + ":admin:invoke", Revision: 1, Audience: "example-producer", Scope: "profiles:delete",
	})
	document := s.authority(t, 1, revision, false, units...)
	s.deliverAuthority(t, document)
	refusals := s.passReasons(t)

	_, present := s.entry()
	require.False(t, present, "a document claiming one unit more than the envelope admits grants nothing at all")
	require.ErrorIs(t, refusals, business.ErrModuleAuthorityOutsideEnvelope)
	require.Contains(t, refusals.Error(), document.Authority, "the refusal names the document, so an operator can act on it")
}

// TestWithdrawingTheAuthorityRemovesThePrincipal: removal is a tombstone
// generation, and the next registry has no entry for what it withdrew.
func TestWithdrawingTheAuthorityRemovesThePrincipal(t *testing.T) {
	s := newRegistryScenario(t, "wdr")
	revision := s.putEnvelope(t, true)
	s.deliverPresence(t, 1, revision)
	s.pass(t)
	s.deliverAuthority(t, s.authority(t, 1, revision, false, s.units("profiles:read")...))
	s.pass(t)
	_, present := s.entry()
	require.True(t, present, "the grant must take effect before its withdrawal can mean anything")

	s.deliverAuthority(t, s.authority(t, 2, revision, true))
	s.pass(t)
	_, present = s.entry()
	require.False(t, present, "a withdrawn authority leaves its principal out of the registry")
	_, err := s.delivery.service.ModuleContentResources(s.prefix)
	require.Error(t, err)
}

// TestAnUnreadableEnvelopeKeepsThePreviousRegistry: a stored revision this
// binary no longer accepts is not "no ceiling". Nothing was judged, so the last
// registry keeps serving rather than every module being withdrawn on a read
// failure — and the next valid revision is applied as usual.
func TestAnUnreadableEnvelopeKeepsThePreviousRegistry(t *testing.T) {
	s := newRegistryScenario(t, "unr")
	revision := s.putEnvelope(t, true)
	s.deliverPresence(t, 1, revision)
	s.pass(t)
	s.deliverAuthority(t, s.authority(t, 1, revision, false, s.units("profiles:read")...))
	s.pass(t)
	_, present := s.entry()
	require.True(t, present)

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
	_, present = s.entry()
	require.True(t, present, "an unreadable ceiling must not withdraw what a readable one established")

	// The next valid revision replaces it, and the document rendered against
	// the old one is outside it.
	s.putEnvelope(t, true)
	s.pass(t)
	_, present = s.entry()
	require.False(t, present)
}

// TestTheEnvelopeIsWrittenOnlyAtTheNextRevision: a writer that read a stale
// revision is refused and writes nothing; a document carrying another revision
// than the one it replaces is refused; a caller who is not a super_admin is
// refused before anything is parsed.
func TestTheEnvelopeIsWrittenOnlyAtTheNextRevision(t *testing.T) {
	s := newRegistryScenario(t, "rev")
	current := s.currentRevision(t)
	_, err := s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, s.envelope(t, current+1, true))
	require.NoError(t, err)

	_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, s.envelope(t, current+1, false))
	var storeErr *business.StoreError
	require.True(t, errors.As(err, &storeErr))
	require.Equal(t, business.ErrTypeConflict, storeErr.StoreErrorType, "a stale expected revision is a conflict, never a second write")

	_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current+1, s.envelope(t, current+3, true))
	require.True(t, errors.As(err, &storeErr))
	require.Equal(t, business.ErrTypeValidation, storeErr.StoreErrorType)

	outsider := seedUser(t)
	_, err = s.delivery.service.PutModuleEnvelope(testCtx, outsider, current+1, s.envelope(t, current+2, true))
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
