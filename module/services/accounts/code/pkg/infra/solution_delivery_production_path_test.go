//go:build !pure

package infra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/internal/deliverytest"
	"accounts/pkg/adapters"
	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// The delivery endpoint, END TO END: the real HTTP handler, the real
// TokenReview carrier check against a fake API server, the real Postgres inbox,
// and the real reconciler reading that inbox. The handler tests beside
// solution_delivery_http.go fake the inbox and the carrier check; these do
// not, so a manifest that cannot actually be delivered, applied and withdrawn
// through the API fails here.

// productionPathSigner is the pinned key these tests sign with, and
// productionPathForeignSigner a key pinned for another ownership domain.
const (
	productionPathSigner        = "example-release"
	productionPathForeignSigner = "globex-release"
	productionPathForeignDomain = "globex"
)

// productionPathDomain is the ownership domain this file's manifests are
// delivered under, and productionPathCoordinate the host they target.
const (
	productionPathDomain     = "acme"
	productionPathCoordinate = "acme/test/eu-west-1"
)

// The host these tests stand in for runs as module "saas" in
// "platform-acme-test-saas", so a module's namespace is
// "platform-acme-test-<module>" by the cell's rule.
const (
	productionPathHostNamespace = "platform-acme-test-saas"
	productionPathHostModule    = "saas"
)

func productionPathNamespace(module string) string { return "platform-acme-test-" + module }

// productionPathDelivery is the whole receiving side, assembled from the real
// pieces.
type productionPathDelivery struct {
	service *business.Service
	handler http.Handler

	// callerNamespace and callerAccount are what the fake API SERVER
	// authenticates the caller as. A fake api server rather than a fake carrier
	// check: the namespace and account comparisons are real.
	callerNamespace string
	callerAccount   string
}

func newProductionPathDelivery(t *testing.T) *productionPathDelivery {
	t.Helper()
	delivery := &productionPathDelivery{callerAccount: "delivery"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" {
			http.NotFound(w, r)
			return
		}
		response := map[string]any{"status": map[string]any{
			"authenticated": true,
			"audiences":     []string{"accounts"},
			"user": map[string]any{
				"username": "system:serviceaccount:" + delivery.callerNamespace + ":" + delivery.callerAccount,
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(server.Close)

	service, err := business.NewService(testStore)
	require.NoError(t, err)
	// A withdrawal is a narrowing of authority, witnessed by the external
	// policy log before it takes effect; the double stands in for the log
	// only, and the receipts go to the real store.
	service.SetPolicyLog(&productionPathPolicyLog{}, testStore)
	service.SetSolutionDelivery(productionVerifier(t), testStore,
		infra.NewSolutionDeliveryCarrierCheck(infra.NewKubernetesClientForTest(t, server)))
	service.SetSolutionDeliveryCoordinate(productionPathCoordinate)
	rule, err := business.NewModuleNamespaceRule(productionPathHostNamespace, productionPathHostModule)
	require.NoError(t, err)
	service.SetModuleNamespaceRule(rule)
	delivery.service = service
	delivery.handler = adapters.NewSolutionDeliveryHTTPHandler(service)
	return delivery
}

// post delivers one carrier, as the Job does.
func (d *productionPathDelivery) post(t *testing.T, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, adapters.SolutionDeliveryManifestPath, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer projected-delivery-token")
	recorder := httptest.NewRecorder()
	d.handler.ServeHTTP(recorder, request)
	return recorder
}

// deliver posts manifest as the delivery account of its module's namespace.
func (d *productionPathDelivery) deliver(t *testing.T, manifest *solutionhost.ModuleManifest) *httptest.ResponseRecorder {
	t.Helper()
	d.callerNamespace, d.callerAccount = productionPathNamespace(manifest.Alias), "delivery"
	return d.post(t, productionPathCarrier(t, manifest))
}

// observe reads the applied state as the protected observer.
func (d *productionPathDelivery) observe(t *testing.T, binding string, want int) *business.SolutionDeliveryApplied {
	t.Helper()
	namespace, account := d.callerNamespace, d.callerAccount
	d.callerNamespace, d.callerAccount = "istio-presenter", "endpoint-publisher"
	defer func() { d.callerNamespace, d.callerAccount = namespace, account }()
	r := httptest.NewRequest(http.MethodGet, adapters.SolutionDeliveryPrefix+"applied/"+binding, nil)
	r.Header.Set("Authorization", "Bearer projected-observer-token")
	w := httptest.NewRecorder()
	d.handler.ServeHTTP(w, r)
	require.Equal(t, want, w.Code, w.Body.String())
	if want != http.StatusOK {
		return nil
	}
	var applied business.SolutionDeliveryApplied
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &applied))
	return &applied
}

// requireDelivered accepts a 200 receipt: "accepted" the first time this
// package's database sees a generation, "replayed" after.
func requireDelivered(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

// A MANIFEST IS DELIVERED, APPLIED AND WITHDRAWN, through the real endpoint,
// the real inbox and the real reconciler.
func TestAManifestIsDeliveredAppliedAndWithdrawnEndToEnd(t *testing.T) {
	solutionID := uniqueAlias("e2e")
	present := productionPathManifest(t, solutionID, 1)
	delivery := newProductionPathDelivery(t)
	delivery.observe(t, present.Binding, http.StatusNotFound)

	recorder := delivery.deliver(t, present)
	requireDelivered(t, recorder)
	var receipt struct {
		Disposition string `json:"disposition"`
		Generation  uint64 `json:"generation"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt))
	require.Equal(t, "accepted", receipt.Disposition)
	require.Equal(t, uint64(1), receipt.Generation)
	delivery.observe(t, present.Binding, http.StatusNotFound) // received is not applied

	reconciler := newProductionPathReconciler(t, delivery.service)
	require.NoError(t, reconciler.RunOnce(testCtx))
	applied := productionPathBindingRecord(t, delivery.service, present.Binding)
	require.NotNil(t, applied.Applied, "the delivered generation was never applied from the inbox")
	require.Equal(t, uint64(1), applied.Applied.Generation)
	require.Equal(t, solutionID, applied.Applied.SolutionID)
	digest, err := present.Digest()
	require.NoError(t, err)
	observed := delivery.observe(t, present.Binding, http.StatusOK)
	require.Equal(t, digest, observed.Digest)
	require.Equal(t, present.Host, observed.Host)
	require.Nil(t, observed.Pending)
	require.NotNil(t, registrationOf(t, delivery.service, solutionID))

	// Delivering the same generation again is a replay; an older or rewritten
	// one is a conflict.
	replay := delivery.deliver(t, present)
	requireDelivered(t, replay)
	require.Contains(t, replay.Body.String(), `"replayed"`)
	rewritten := productionPathManifest(t, solutionID, 1)
	rewritten.Release.Version = "9.9.9"
	require.Equal(t, http.StatusConflict, delivery.deliver(t, rewritten).Code)

	// THE WITHDRAWAL, carried by the delivery account of the module it
	// withdraws.
	withdrawal := productionPathWithdrawal(t, present, 2)
	requireDelivered(t, delivery.deliver(t, withdrawal))
	require.False(t, delivery.observe(t, present.Binding, http.StatusOK).Removed, "received is not applied")
	require.NoError(t, reconciler.RunOnce(testCtx))
	withdrawn := productionPathBindingRecord(t, delivery.service, present.Binding)
	require.True(t, withdrawn.Applied.Removed, "the withdrawal did not apply")
	require.Equal(t, solutionID, withdrawn.Applied.SolutionID, "a withdrawal keeps the key it withdrew")
	require.True(t, delivery.observe(t, present.Binding, http.StatusOK).Removed)
	require.NotNil(t, registrationOf(t, delivery.service, solutionID).TombstonedAt,
		"the registration is still live after the binding was withdrawn")

	// A replay after a restart is carried from the same namespace and nowhere
	// else, and the binding never comes back.
	restarted := newProductionPathDelivery(t)
	requireDelivered(t, restarted.deliver(t, withdrawal))
	restarted.callerNamespace = productionPathNamespace("unrelated")
	require.Equal(t, http.StatusForbidden, restarted.post(t, productionPathCarrier(t, withdrawal)).Code)
	revived := productionPathManifest(t, solutionID, 3)
	require.Equal(t, http.StatusConflict, restarted.deliver(t, revived).Code)
}

// The carrier is the `delivery` account of the manifest's module namespace by
// the cell's rule, and nothing else: another account in that namespace,
// another module's delivery account, and the bare module namespace are each
// refused, and nothing reaches the inbox.
func TestOnlyTheModulesDeliveryAccountCarriesItsManifest(t *testing.T) {
	manifest := productionPathManifest(t, uniqueAlias("carrier"), 1)
	delivery := newProductionPathDelivery(t)
	for name, caller := range map[string][2]string{
		"another account":           {productionPathNamespace(manifest.Alias), "default"},
		"another module's delivery": {productionPathNamespace("billing"), "delivery"},
		"the bare module namespace": {manifest.Alias, "delivery"},
	} {
		t.Run(name, func(t *testing.T) {
			delivery.callerNamespace, delivery.callerAccount = caller[0], caller[1]
			recorder := delivery.post(t, productionPathCarrier(t, manifest))
			require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), productionPathNamespace(manifest.Alias)+"/delivery")
		})
	}
	var newest []*business.SolutionDeliveryRecord
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		newest, err = testStore.ListNewestDeliveredDocuments(ctx)
		return err
	}))
	for _, record := range newest {
		require.NotEqual(t, manifest.Binding, record.Binding, "a refused carrier reached the inbox")
	}
}

func registrationOf(t *testing.T, service *business.Service, solutionID string) *business.SolutionRegistration {
	t.Helper()
	registrations, _, err := service.ListSolutionRegistrations(testCtx, true)
	require.NoError(t, err)
	for _, registration := range registrations {
		if registration.SolutionID == solutionID {
			return registration
		}
	}
	return nil
}

// newProductionPathReconciler builds the reconciler over the durable inbox,
// which is the only source there is.
func newProductionPathReconciler(t *testing.T, service *business.Service) *business.SolutionHostBindingReconciler {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(service,
		business.SolutionHostBindingReconcilerConfig{
			Source:     business.NewDeliveredSolutionHostBindings(service),
			Verifier:   productionVerifier(t),
			Coordinate: productionPathCoordinate,
			Interval:   time.Minute,
		})
	require.NoError(t, err)
	return reconciler
}

func productionPathBindingRecord(t *testing.T, service *business.Service, bindingID string) *business.SolutionHostBindingRecord {
	t.Helper()
	records, err := service.ListSolutionHostBindings(testCtx)
	require.NoError(t, err)
	for _, record := range records {
		if record.BindingID == bindingID {
			return record
		}
	}
	t.Fatalf("no durable record for binding %q", bindingID)
	return nil
}

// productionPathManifest is a live manifest for one module on this test host:
// one workload, one routed endpoint.
func productionPathManifest(t *testing.T, alias string, generation uint64) *solutionhost.ModuleManifest {
	t.Helper()
	manifest := &solutionhost.ModuleManifest{
		Schema: solutionhost.SchemaModuleManifestV1, Binding: "acme.test." + alias, Alias: alias,
		Kind: solutionhost.KindSolution, Generation: generation,
		Host:      solutionhost.HostTarget{Coordinate: productionPathCoordinate, Domain: productionPathDomain},
		Release:   solutionhost.Release{Publisher: "acme", Name: alias, Version: "1.4.0"},
		Workloads: []solutionhost.Workload{{Service: "api", Container: "api"}},
		Endpoints: []solutionhost.Endpoint{{Name: "rest", Service: "api", API: "rest", Address: "http://api." + productionPathNamespace(alias) + ".svc.cluster.local:8080"}},
	}
	require.NoError(t, manifest.Validate(), "the fixture must be a valid manifest")
	return manifest
}

// productionPathWithdrawal is the withdrawal of a manifest at generation.
func productionPathWithdrawal(t *testing.T, manifest *solutionhost.ModuleManifest, generation uint64) *solutionhost.ModuleManifest {
	t.Helper()
	removed := *manifest
	removed.Generation, removed.Removed = generation, true
	removed.Workloads, removed.Endpoints, removed.Authority, removed.Frontend, removed.Group = nil, nil, nil, nil, nil
	require.NoError(t, removed.Validate(), "the withdrawal must be a valid manifest")
	return &removed
}

// productionPathCarrier is the carrier delivery POSTs: the canonical bytes,
// which are the signing input, and a DSSE envelope over them.
func productionPathCarrier(t *testing.T, manifest *solutionhost.ModuleManifest) []byte {
	t.Helper()
	payload, err := solutionhost.SignedPayload(manifest)
	require.NoError(t, err)
	return deliverytest.Carrier(t, payload, productionPathSigner)
}

// productionPathPolicyLog witnesses a narrowing, so a withdrawal can take
// effect. A RECORDING double: the operation id is what the host's idempotency
// rests on, so it is honoured.
type productionPathPolicyLog struct {
	mu       sync.Mutex
	sequence uint64
	records  []*business.PolicyLogRecord
}

func (l *productionPathPolicyLog) Append(_ context.Context, entry *business.PolicyLogEntry) (*business.PolicyLogReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, record := range l.records {
		if record.OperationID == entry.OperationID {
			return &business.PolicyLogReceipt{Receipt: record.Receipt, Sequence: record.Sequence, AppendedAt: record.LoggedAt}, nil
		}
	}
	l.sequence++
	receipt := &business.PolicyLogReceipt{
		Receipt: "delivery-test-receipt-" + entry.OperationID, Sequence: l.sequence, AppendedAt: time.Now().UTC(),
	}
	copied := *entry
	l.records = append(l.records, &business.PolicyLogRecord{
		PolicyLogEntry: copied, Receipt: receipt.Receipt, Sequence: receipt.Sequence, LoggedAt: receipt.AppendedAt,
	})
	return receipt, nil
}

func (l *productionPathPolicyLog) Entries(_ context.Context, after uint64, _ int) ([]*business.PolicyLogRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*business.PolicyLogRecord
	for _, record := range l.records {
		if record.Sequence > after {
			out = append(out, record)
		}
	}
	return out, nil
}

// wireNarrowingPolicyLog gives a test service the policy log's two halves, so
// a narrowing can actually happen: WithPolicyLoggedNarrowing refuses on a host
// with no log, by design.
func wireNarrowingPolicyLog(service *business.Service) *business.Service {
	service.SetPolicyLog(&productionPathPolicyLog{}, testStore)
	return service
}

func productionVerifier(t *testing.T) *solutionhost.Verifier {
	t.Helper()
	return deliverytest.Verifier(t, map[string][]string{
		productionPathSigner:        {productionPathDomain},
		productionPathForeignSigner: {productionPathForeignDomain},
	})
}

// THE HOST'S OWN RULES ARE ENFORCED AT DELIVERY, through the real endpoint:
// a second manifest for a workload a delivered binding holds is a 409 naming
// that binding, a foreign domain's claim on a held principal is a 403, and an
// endpoint that is not its workload's in-cluster Service is a 422. Nothing
// refused reaches the inbox.
func TestTheHostsRulesAreEnforcedAtDelivery(t *testing.T) {
	d := newProductionPathDelivery(t)

	holder := productionPathManifest(t, "ruleheld", 1)
	holder.Authority = &solutionhost.Authority{Principal: "ruleheld"}
	requireDelivered(t, d.deliver(t, holder))

	second := productionPathManifest(t, "ruleheld", 1)
	second.Binding = "acme.test.ruleheld-second"
	recorder := d.deliver(t, second)
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `held by binding \"acme.test.ruleheld\"`)

	// One principal is one live binding within a domain too.
	samePrincipal := productionPathManifest(t, "ruleshared", 1)
	samePrincipal.Authority = &solutionhost.Authority{Principal: "ruleheld"}
	recorder = d.deliver(t, samePrincipal)
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `principal \"ruleheld\" is held by binding \"acme.test.ruleheld\"`)

	foreign := productionPathManifest(t, "ruleforeign", 1)
	foreign.Binding, foreign.Host.Domain, foreign.Release.Publisher = "globex.test.ruleforeign", productionPathForeignDomain, "globex"
	foreign.Authority = &solutionhost.Authority{Principal: "ruleheld"}
	payload, err := solutionhost.SignedPayload(foreign)
	require.NoError(t, err)
	d.callerNamespace, d.callerAccount = productionPathNamespace(foreign.Alias), "delivery"
	recorder = d.post(t, deliverytest.Carrier(t, payload, productionPathForeignSigner))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `principal \"ruleheld\" is held by binding \"acme.test.ruleheld\"`)

	for _, address := range []string{
		"http://169.254.169.254:80",
		"http://api." + productionPathNamespace("another") + ".svc.cluster.local:8080",
	} {
		escaping := productionPathManifest(t, "ruleaddress", 1)
		escaping.Endpoints[0].Address = address
		recorder = d.deliver(t, escaping)
		require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, address+": "+recorder.Body.String())
	}
}

// A BINDING KEEPS ITS DOMAIN WHEN ITS LIVE STATE IS GONE. Migration 41 cleared
// every live binding's applied manifest and kept the domain it applied under
// (first_domain); until the owner redelivers, another pinned domain's
// manifest for that binding id is refused, and the owner's is not. A binding
// the host applies records its domain the same way.
func TestABindingAwaitingRedeliveryKeepsItsDomain(t *testing.T) {
	d := newProductionPathDelivery(t)
	const binding = "acme.test.ruletakeover"
	// The state migration 41 leaves: no applied manifest, the domain kept.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.SaveSolutionHostBinding(ctx, &business.SolutionHostBindingRecord{
			BindingID: binding, HostCoordinate: productionPathCoordinate, Domain: productionPathDomain, UpdatedAt: time.Now().UTC(),
		})
	}))

	takeover := productionPathManifest(t, "ruletakeover", 1)
	takeover.Binding, takeover.Host.Domain, takeover.Release.Publisher = binding, productionPathForeignDomain, "globex"
	payload, err := solutionhost.SignedPayload(takeover)
	require.NoError(t, err)
	d.callerNamespace, d.callerAccount = productionPathNamespace(takeover.Alias), "delivery"
	recorder := d.post(t, deliverytest.Carrier(t, payload, productionPathForeignSigner))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `binding \"acme.test.ruletakeover\" belongs to ownership domain \"acme\"`)

	owner := productionPathManifest(t, "ruletakeover", 1)
	owner.Binding = binding
	requireDelivered(t, d.deliver(t, owner))
	require.NoError(t, newProductionPathReconciler(t, d.service).RunOnce(testCtx))
	record := productionPathBindingRecord(t, d.service, binding)
	require.NotNil(t, record.Applied, record.PendingReason)
	require.Equal(t, productionPathDomain, record.Domain)

	fresh := productionPathManifest(t, "ruledomainrecorded", 1)
	requireDelivered(t, d.deliver(t, fresh))
	require.NoError(t, newProductionPathReconciler(t, d.service).RunOnce(testCtx))
	require.Equal(t, productionPathDomain, productionPathBindingRecord(t, d.service, fresh.Binding).Domain,
		"an applied binding records the domain it applied under")
}
