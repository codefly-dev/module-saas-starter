package business

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
)

// A host with applied module manifests and no module envelope denies every
// module's mint. These tests hold the host to saying so, by name, in its own
// log: the caller is told only "denied", so a reason that is not logged here is
// a reason that exists nowhere.

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

// noDocuments is a delivery inbox that holds nothing new this pass.
type noDocuments struct{}

func (noDocuments) Documents(context.Context) ([]SolutionHostBindingDocument, error) { return nil, nil }

// appliedRecord is the binding record of an applied, live manifest.
func appliedRecord(t *testing.T, manifest *solutionhost.ModuleManifest) *SolutionHostBindingRecord {
	t.Helper()
	document, err := solutionhost.Marshal(manifest)
	require.NoError(t, err)
	return &SolutionHostBindingRecord{
		BindingID: manifest.Binding,
		Applied: &SolutionHostBindingApplied{SolutionHostBindingGeneration: SolutionHostBindingGeneration{
			Generation: manifest.Generation, Document: string(document),
		}},
	}
}

// missingEnvelopeHost is a host whose one applied manifest asks for authority
// under testModulePrefix, with the given envelope source.
func missingEnvelopeHost(t *testing.T, envelopes ModuleEnvelopeSource, records ...*SolutionHostBindingRecord) *SolutionHostBindingReconciler {
	t.Helper()
	service := &Service{store: appliedBindingsStore{records: records}}
	reconciler := &SolutionHostBindingReconciler{
		service: service, source: noDocuments{}, coordinate: "acme-test", interval: time.Hour,
		now: func() time.Time { return time.Now().UTC() }, trigger: make(chan struct{}, 1),
	}
	reconciler.modulePrincipals = &ModulePrincipalReconciler{reconciler: reconciler, envelopes: envelopes}
	return reconciler
}

func TestAPassWithNoEnvelopeNamesEveryManifestAskingForAuthority(t *testing.T) {
	asking := appliedRecord(t, testManifest(t, map[string][]string{"invoke": {"profiles:read"}}))
	quiet := testManifest(t, nil)
	quiet.Binding, quiet.Alias, quiet.Authority = "acme.test.quiet", "quiet", nil
	quiet.Workloads = []solutionhost.Workload{{Service: "quiet", Container: "app"}}

	t.Run("no envelope written", func(t *testing.T) {
		err := missingEnvelopeHost(t, noEnvelopeWritten{}, asking, appliedRecord(t, quiet)).modulePrincipals.RunOnce(context.Background())
		require.ErrorIs(t, err, ErrModuleEnvelopeMissing)
		require.Contains(t, err.Error(), `1 applied manifests ask for authority ("reports" (binding "acme.test.reports" generation 1))`)
		require.Contains(t, err.Error(), "no module envelope has been written; every module is denied until a platform super_admin writes one")
		require.NotContains(t, err.Error(), "quiet", "a manifest asking for nothing is denied nothing")
	})
	t.Run("no envelope store", func(t *testing.T) {
		err := missingEnvelopeHost(t, noEnvelopeStore{}, asking).modulePrincipals.RunOnce(context.Background())
		require.ErrorIs(t, err, ErrModuleEnvelopeMissing)
		require.Contains(t, err.Error(), "this host has no module envelope store")
	})
	t.Run("nothing asks: nothing to say", func(t *testing.T) {
		require.NoError(t, missingEnvelopeHost(t, noEnvelopeWritten{}, appliedRecord(t, quiet)).modulePrincipals.RunOnce(context.Background()))
	})
}

// The pass's reasons reach the host's log on every pass, by the loop that runs
// it — the same line an omitted principal is reported on.
func TestTheMissingEnvelopeIsLoggedEachPass(t *testing.T) {
	logs := captureBusinessLogs(t)
	host := missingEnvelopeHost(t, noEnvelopeWritten{}, appliedRecord(t, testManifest(t, map[string][]string{"invoke": {"profiles:read"}})))
	host.Start(context.Background())
	host.Trigger()
	require.Eventually(t, func() bool { return strings.Count(logs(), "no module envelope has been written") >= 2 },
		5*time.Second, 10*time.Millisecond, "the first pass and the triggered one each say it")
	require.NoError(t, host.Shutdown(context.Background()))
	require.Contains(t, logs(), "solution host binding reconcile pass failed")
	require.Contains(t, logs(), `"reports" (binding "acme.test.reports" generation 1)`)
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
