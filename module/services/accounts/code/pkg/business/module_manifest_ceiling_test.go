package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/internal/deliverytest"
)

// A host no module envelope was written to grants each applied, signed module
// manifest what it asks for: the ceiling is the manifest. These tests hold the
// registry pass to that, to the verification that still stands in front of it,
// and to the envelope narrowing where one IS written.

// appliedBindingsStore serves fixed applied host bindings to the reconcile
// pass's snapshot read.
type appliedBindingsStore struct {
	reviewControlPlaneStore
	records []*SolutionHostBindingRecord
}

func (appliedBindingsStore) WithControlPlaneReadSnapshot(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s appliedBindingsStore) ListSolutionHostBindings(context.Context) ([]*SolutionHostBindingRecord, error) {
	return s.records, nil
}

// noEnvelopeWritten is an envelope store no administrator has written to.
type noEnvelopeWritten struct{}

func (noEnvelopeWritten) CurrentModuleEnvelope(context.Context) (*ModuleEnvelope, error) {
	return nil, nil
}

// noEnvelopeStore is the service's own source on a host no envelope store was
// wired into.
type noEnvelopeStore struct{}

func (noEnvelopeStore) CurrentModuleEnvelope(context.Context) (*ModuleEnvelope, error) {
	return nil, ErrModuleEnvelopeUnavailable
}

// writtenEnvelope is an envelope store holding one revision.
type writtenEnvelope struct{ envelope *ModuleEnvelope }

func (w writtenEnvelope) CurrentModuleEnvelope(context.Context) (*ModuleEnvelope, error) {
	return w.envelope, nil
}

// unreadableEnvelope is an envelope store that cannot answer.
type unreadableEnvelope struct{}

func (unreadableEnvelope) CurrentModuleEnvelope(context.Context) (*ModuleEnvelope, error) {
	return nil, errors.New("the store is down")
}

// noDocuments is a delivery inbox that holds nothing new this pass.
type noDocuments struct{}

func (noDocuments) Documents(context.Context) ([]SolutionHostBindingDocument, error) { return nil, nil }

// signedInbox holds each applied generation's signed carrier, as the delivery
// inbox does, so the pass re-verifies what it grants.
type signedInbox struct {
	SolutionDeliveryStore
	rows map[string]*SolutionDeliveryRecord
}

func (i signedInbox) ReadDeliveredDocument(_ context.Context, binding string, generation uint64) (*SolutionDeliveryRecord, error) {
	return i.rows[fmt.Sprintf("%s@%d", binding, generation)], nil
}

const (
	ceilingReleaseKey  = "ceiling-release"
	ceilingUnpinnedKey = "ceiling-unpinned"
)

// deliveredHost is a host whose applied manifests are each signed in its inbox
// by keyID, under a verifier that pins ceilingReleaseKey alone.
func deliveredHost(t *testing.T, envelopes ModuleEnvelopeSource, keyID string, manifests ...*solutionhost.ModuleManifest) *SolutionHostBindingReconciler {
	t.Helper()
	inbox := signedInbox{rows: map[string]*SolutionDeliveryRecord{}}
	var records []*SolutionHostBindingRecord
	for _, manifest := range manifests {
		payload, err := solutionhost.SignedPayload(manifest)
		require.NoError(t, err)
		inbox.rows[fmt.Sprintf("%s@%d", manifest.Binding, manifest.Generation)] = &SolutionDeliveryRecord{
			Binding: manifest.Binding, Generation: manifest.Generation, Payload: payload, Envelope: deliverytest.Envelope(t, payload, keyID),
		}
		document, err := solutionhost.Marshal(manifest)
		require.NoError(t, err)
		digest, err := manifest.Digest()
		require.NoError(t, err)
		records = append(records, &SolutionHostBindingRecord{
			BindingID: manifest.Binding,
			Applied: &SolutionHostBindingApplied{SolutionHostBindingGeneration: SolutionHostBindingGeneration{
				Generation: manifest.Generation, Document: string(document), Digest: digest,
			}},
		})
	}
	service := &Service{store: appliedBindingsStore{records: records}, deliveryStore: inbox}
	reconciler := &SolutionHostBindingReconciler{
		service: service, source: noDocuments{}, coordinate: "acme-test", interval: time.Hour,
		verifier: deliverytest.Verifier(t, map[string][]string{ceilingReleaseKey: {"acme-test"}}),
		now:      func() time.Time { return time.Now().UTC() }, trigger: make(chan struct{}, 1),
	}
	reconciler.modulePrincipals = &ModulePrincipalReconciler{reconciler: reconciler, envelopes: envelopes}
	return reconciler
}

// quietManifest is a live manifest that asks for no authority.
func quietManifest(t *testing.T) *solutionhost.ModuleManifest {
	t.Helper()
	quiet := testManifest(t, nil)
	quiet.Binding, quiet.Alias, quiet.Authority = "acme.test.quiet", "quiet", nil
	quiet.Workloads = []solutionhost.Workload{{Service: "quiet", Container: "app"}}
	return quiet
}

func TestWithNoEnvelopeTheSignedManifestIsTheCeiling(t *testing.T) {
	asking := testManifest(t, map[string][]string{"invoke": {"profiles:read", "profiles:write"}, "lookup": {"profiles:read"}})
	for name, envelopes := range map[string]ModuleEnvelopeSource{
		"no envelope written": noEnvelopeWritten{},
		"no envelope store":   noEnvelopeStore{},
		"no envelope source":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			host := deliveredHost(t, envelopes, ceilingReleaseKey, asking, quietManifest(t))
			require.NoError(t, host.modulePrincipals.RunOnce(context.Background()))
			registry := host.service.ModulePrincipals()
			require.Len(t, registry, 1, "a manifest asking for nothing holds no principal")
			grant := registry[ModulePrincipalID(testModulePrefix)]
			require.Equal(t, testModulePrefix, grant.Prefix)
			require.Equal(t, testPresenceBinding, grant.PresenceBinding)
			require.Equal(t, uint64(1), grant.PresenceGeneration)
			require.Equal(t, []string{"reports.render"}, grant.Queues)
			require.Equal(t, []string{"reports"}, grant.Namespaces)
			require.Equal(t, []string{"reports"}, grant.Resources)
			require.Empty(t, grant.Tenant, "no single tenant: each capability names its organization and installation")
			require.False(t, grant.CrossTenant)
			require.Empty(t, grant.ArtifactPolicies)
			require.Empty(t, grant.ReadAudiences)
			require.Equal(t, []ModuleWorkload{{Namespace: testModulePrefix, ServiceAccount: "reports", Container: "app"}}, grant.Workloads)
			run := grant.OperationAudiences["run"]
			require.Equal(t, testProducerAudience, run.Audience)
			require.Empty(t, run.DelegationMode)
			require.Equal(t, []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read", "write"}}}, run.InvokeScopes)
			require.Equal(t, []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}}}, run.LookupScopes)
			require.Nil(t, run.HeadlessScopes, "the manifest asks for no headless scope, so none is granted")
			require.Equal(t, run.InvokeScopes, run.SourceDelegationScopes, "its one binding accepts a delegation")
			require.Empty(t, host.modulePrincipals.WithheldAuthorityReasons())
		})
	}
}

// Verification still stands in front of the ceiling: a carrier the pinned keys
// no longer verify grants nothing, envelope or not.
func TestWithNoEnvelopeAnUnverifiedManifestGrantsNothing(t *testing.T) {
	host := deliveredHost(t, noEnvelopeWritten{}, ceilingUnpinnedKey, testManifest(t, map[string][]string{"invoke": {"profiles:read"}}))
	err := host.modulePrincipals.RunOnce(context.Background())
	require.ErrorIs(t, err, ErrModulePrincipalOmitted)
	require.Contains(t, err.Error(), `binding "acme.test.reports" generation 1 grants nothing`)
	require.Empty(t, host.service.ModulePrincipals())
}

// A read failure never adds authority: an envelope store that cannot answer is
// not read as "none written", so the manifests do not stand alone.
func TestAnUnreadableEnvelopeIsNotReadAsNone(t *testing.T) {
	host := deliveredHost(t, unreadableEnvelope{}, ceilingReleaseKey, testManifest(t, map[string][]string{"invoke": {"profiles:read"}}))
	err := host.modulePrincipals.RunOnce(context.Background())
	require.ErrorIs(t, err, ErrModulePrincipalInputsUnreadable)
	require.Empty(t, host.service.ModulePrincipals())
}

// Where a platform administrator has written an envelope, it narrows the
// manifest exactly as before.
func TestAWrittenEnvelopeNarrowsTheManifest(t *testing.T) {
	asking := testManifest(t, map[string][]string{"invoke": {"profiles:read", "profiles:delete"}})
	t.Run("an envelope that names the principal", func(t *testing.T) {
		host := deliveredHost(t, writtenEnvelope{parseTestEnvelope(t)}, ceilingReleaseKey, asking)
		require.NoError(t, host.modulePrincipals.RunOnce(context.Background()))
		grant := host.service.ModulePrincipals()[ModulePrincipalID(testModulePrefix)]
		require.Equal(t, []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}},
			grant.OperationAudiences["run"].InvokeScopes, "delete is the manifest's ask alone")
		require.Equal(t, testEnvelopeTenant, grant.Tenant)
	})
	t.Run("an envelope that does not", func(t *testing.T) {
		other := &ModuleEnvelope{Revision: 1, Principals: map[string]ModuleEnvelopePrincipal{"elsewhere": {}}}
		host := deliveredHost(t, writtenEnvelope{other}, ceilingReleaseKey, asking)
		err := host.modulePrincipals.RunOnce(context.Background())
		require.ErrorIs(t, err, ErrModulePrincipalOmitted)
		require.Contains(t, err.Error(), "the administrative policy declares no grant for it")
		require.Empty(t, host.service.ModulePrincipals())
	})
}

// The mint's refusal of a prefix the registry does not hold names the prefix
// and the reason in the host's log, as the refusal of an execution does.
func TestAnUndeclaredPrefixIsRefusedWithItsReasonLogged(t *testing.T) {
	for name, tc := range map[string]struct {
		registry ModulePrincipalRegistry
		revision uint64
		reason   string
	}{
		"no envelope ever derived":     {ModulePrincipalRegistry{}, 0, "the module registry is empty and was derived from no module envelope"},
		"an envelope allowing nothing": {ModulePrincipalRegistry{}, 4, "the module registry is empty"},
		"other modules declared":       {ModulePrincipalRegistry{"principal-1": {Prefix: "crm"}}, 4, "the module registry holds no entry for it"},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureBusinessLogs(t)
			s := reviewService()
			s.InstallDerivedModulePrincipals(tc.registry, tc.revision, nil)
			_, _, err := s.authorizeModuleIdentity(context.Background(), s.moduleRegistrySnapshot(), "documents", "projected-token")
			require.ErrorIs(t, err, ErrModuleIdentityDenied)
			require.Contains(t, err.Error(), tc.reason)
			logged := logs()
			require.Contains(t, logged, "module identity refused")
			require.Contains(t, logged, "documents")
			require.Contains(t, logged, tc.reason)
		})
	}
}

// captureBusinessLogs redirects wool's fallback logger — the one Get() uses
// for a context carrying no provider — and returns a reader of what was
// logged so far.
func captureBusinessLogs(t *testing.T) func() string {
	t.Helper()
	capture := &businessLogCapture{}
	wool.SetFallbackLogger(capture)
	t.Cleanup(func() { wool.SetFallbackLogger(nil) })
	return capture.String
}

type businessLogCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *businessLogCapture) Process(msg *wool.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, msg.String())
}

func (c *businessLogCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

// roleOnlyStore answers the platform role and nothing else: reaching the
// applied bindings without super_admin panics on the nil embedded store.
type roleOnlyStore struct {
	Store
	role string
}

func (s roleOnlyStore) GetPlatformRole(context.Context, string) (string, error) { return s.role, nil }

// What the applied manifests ask for is a super_admin's read on its own, not
// only behind the adapter's gate.
func TestOnlyASuperAdminReadsWhatTheManifestsAskFor(t *testing.T) {
	for _, role := range []string{"", "support", "admin"} {
		t.Run("role "+role, func(t *testing.T) {
			s := &Service{store: roleOnlyStore{role: role}}
			_, err := s.ModuleAuthorityRequests(context.Background(), "actor-1")
			var refused *StoreError
			require.ErrorAs(t, err, &refused)
			require.Equal(t, ErrTypePermission, refused.StoreErrorType)
		})
	}
}

// A replica must refuse its old authority as soon as another replica commits
// a newer generation, before its own registry reconcile has run.
func TestAManifestGenerationFencesAnOlderRegistrySnapshot(t *testing.T) {
	host := deliveredHost(t, nil, ceilingReleaseKey, testManifest(t, map[string][]string{"invoke": {"profiles:read"}}))
	require.NoError(t, host.modulePrincipals.RunOnce(context.Background()))
	snapshot := host.service.moduleRegistrySnapshot()
	require.NoError(t, host.service.fenceModuleRegistrySnapshot(context.Background(), snapshot))
	records := host.service.store.(appliedBindingsStore).records
	records[0].Applied.Generation++
	err := host.service.fenceModuleRegistrySnapshot(context.Background(), snapshot)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, err.Error(), testPresenceBinding)
	require.Contains(t, err.Error(), "registry generation 1 differs from applied generation 2")
}
