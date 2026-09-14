package business_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/githubconnector"
)

const (
	testAppInstallation      = "4242"
	testOtherAppInstallation = "9999"
)

// fakeInstallationAPI answers the two app-authenticated reads the reconciler
// makes: which installation covers a repository, and whether that installation
// is suspended. Whatever a delivery claimed, this is what the host acts on.
type fakeInstallationAPI struct {
	mu sync.Mutex
	// coverage maps "owner/name" to the installation that still covers it; a
	// repository absent here has been deselected or the App uninstalled.
	coverage map[string]string
	// installations maps an installation id to its suspension instant (nil when
	// active); an id absent here has been deleted.
	installations map[string]*time.Time
	// failStatus, when set, fails every request — GitHub being unavailable.
	failStatus int

	coverageReads     int
	installationReads int
}

func (f *fakeInstallationAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStatus != 0 {
		http.Error(w, `{"message":"unavailable"}`, f.failStatus)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/installation"):
		f.coverageReads++
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/installation")
		installation, ok := f.coverage[repo]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		writeAppJSON(w, http.StatusOK, map[string]any{"id": installation})
	case strings.HasPrefix(r.URL.Path, "/app/installations/"):
		f.installationReads++
		id := strings.TrimPrefix(r.URL.Path, "/app/installations/")
		suspendedAt, ok := f.installations[id]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		body := map[string]any{"id": id, "suspended_at": nil}
		if suspendedAt != nil {
			body["suspended_at"] = suspendedAt.Format(time.RFC3339)
		}
		writeAppJSON(w, http.StatusOK, body)
	default:
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}
}

func (f *fakeInstallationAPI) reads() (coverage, installations int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.coverageReads, f.installationReads
}

type installationHarness struct {
	svc   *business.Service
	store *datasourceFakeStore
	api   *fakeInstallationAPI
}

func newInstallationHarness(t *testing.T, api *fakeInstallationAPI) *installationHarness {
	t.Helper()
	if api.coverage == nil {
		api.coverage = map[string]string{}
	}
	if api.installations == nil {
		api.installations = map[string]*time.Time{}
	}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, &recordingProducer{}, nil)
	svc.SetGitHubConnector(githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL)))
	svc.SetGitHubAppRegistration("123456", testAppKeyPEM(t), "whsec_app")
	return &installationHarness{svc: svc, store: store, api: api}
}

// seedAppSource writes an App-backed source straight into the store, bound to
// an installation and carrying ingested history the reconciler must not touch.
func (h *installationHarness) seedAppSource(t *testing.T, id, repo, installationID string) *business.DatasourceSource {
	t.Helper()
	source := &business.DatasourceSource{
		ID:                   id,
		OrgID:                testOrg,
		Provider:             business.DatasourceProviderGitHub,
		Repo:                 repo,
		Branch:               "main",
		BoundaryNodeID:       "boundary-" + id,
		CredentialSecretRef:  "envelope-" + id,
		Status:               business.DatasourceStatusActive,
		GitHubInstallationID: installationID,
		ReconcileInterval:    30 * time.Minute,
		LastIngestedCommit:   "commit-" + id,
	}
	require.NoError(t, h.store.InsertDatasourceSource(context.Background(), source))
	return source
}

func (h *installationHarness) reload(t *testing.T, id string) *business.DatasourceSource {
	t.Helper()
	source, err := h.store.GetDatasourceSourceByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, source)
	return source
}

func (h *installationHarness) degrade(t *testing.T, id, reason string) {
	t.Helper()
	require.NoError(t, h.store.MarkDatasourceSourceDegraded(context.Background(), id, reason))
}

func (h *installationHarness) reconcile(t *testing.T, installationID string) error {
	t.Helper()
	return h.svc.ReconcileGitHubInstallation(context.Background(), installationID)
}

// Losing a repository parks the source and says why — and leaves everything
// else on the row alone. Revocation is not deletion: the ingest cursor,
// boundary and identity survive, so restoring access resumes rather than
// re-ingests, and retained content stays governed by the tenant's own policy.
func TestReconcileInstallationParksDeselectedRepository(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{
		installations: map[string]*time.Time{testAppInstallation: nil},
	})
	source := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)

	require.NoError(t, h.reconcile(t, testAppInstallation))

	parked := h.reload(t, source.ID)
	require.Equal(t, business.DatasourceStatusDegraded, parked.Status)
	require.Equal(t, business.DatasourceReasonInstallationRepositoryUnavailable, parked.StatusReason)
	require.Equal(t, "commit-source-a", parked.LastIngestedCommit, "parking must not discard ingested history")
	require.Equal(t, "boundary-source-a", parked.BoundaryNodeID)
	require.Nil(t, parked.NextReconcileAt, "a parked source leaves the reconcile sweep")
}

func TestReconcileInstallationParksSuspendedInstallation(t *testing.T) {
	suspended := time.Now().UTC()
	h := newInstallationHarness(t, &fakeInstallationAPI{
		coverage:      map[string]string{"acme/docs": testAppInstallation},
		installations: map[string]*time.Time{testAppInstallation: &suspended},
	})
	source := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)

	require.NoError(t, h.reconcile(t, testAppInstallation))

	parked := h.reload(t, source.ID)
	require.Equal(t, business.DatasourceStatusDegraded, parked.Status)
	require.Equal(t, business.DatasourceReasonInstallationSuspended, parked.StatusReason)
}

// The delivery is a claim, not an instruction. A replayed or late "deleted"
// arrives here as nothing more than an installation id, and GitHub says access
// is intact — so the source keeps syncing.
func TestReconcileInstallationTrustsGitHubOverTheDelivery(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{
		coverage:      map[string]string{"acme/docs": testAppInstallation},
		installations: map[string]*time.Time{testAppInstallation: nil},
	})
	source := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)

	require.NoError(t, h.reconcile(t, testAppInstallation))

	require.Equal(t, business.DatasourceStatusActive, h.reload(t, source.ID).Status)
}

// Restored access lifts the park this path applied, and restores the reconcile
// schedule so the source starts syncing again on its own.
func TestReconcileInstallationRestoresRepairedSource(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{
		coverage:      map[string]string{"acme/docs": testAppInstallation},
		installations: map[string]*time.Time{testAppInstallation: nil},
	})
	source := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)
	h.degrade(t, source.ID, business.DatasourceReasonInstallationSuspended)

	require.NoError(t, h.reconcile(t, testAppInstallation))

	restored := h.reload(t, source.ID)
	require.Equal(t, business.DatasourceStatusActive, restored.Status)
	require.Empty(t, restored.StatusReason)
	require.NotNil(t, restored.NextReconcileAt, "a restored source rejoins the reconcile sweep")
}

// Two paths degrade a source. Restored App access must not clear a park the
// change-set compiler applied for a structural fault of its own.
func TestReconcileInstallationLeavesAnotherPathsDegradeAlone(t *testing.T) {
	const compilerReason = "snapshot manifest is 1048576 bytes, over the 983040-byte ingest limit"
	h := newInstallationHarness(t, &fakeInstallationAPI{
		coverage:      map[string]string{"acme/docs": testAppInstallation},
		installations: map[string]*time.Time{testAppInstallation: nil},
	})
	source := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)
	h.degrade(t, source.ID, compilerReason)

	require.NoError(t, h.reconcile(t, testAppInstallation))

	untouched := h.reload(t, source.ID)
	require.Equal(t, business.DatasourceStatusDegraded, untouched.Status)
	require.Equal(t, compilerReason, untouched.StatusReason)
}

// An operator pause outranks a webhook: losing access must not rewrite a source
// somebody deliberately stopped.
func TestReconcileInstallationDoesNotOverwriteOperatorPause(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{
		installations: map[string]*time.Time{testAppInstallation: nil},
	})
	source := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)
	source.Status = business.DatasourceStatusPaused
	require.NoError(t, h.store.InsertDatasourceSource(context.Background(), source))

	require.NoError(t, h.reconcile(t, testAppInstallation))

	require.Equal(t, business.DatasourceStatusPaused, h.reload(t, source.ID).Status)
}

// The routing column is an index, not an authority. A source re-bound to
// another installation since the column was stamped is answered for the
// installation that actually covers its repository, so a stale column costs a
// redundant check rather than a wrong revocation.
func TestReconcileInstallationFollowsRebindingRatherThanTheColumn(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{
		coverage:      map[string]string{"acme/docs": testOtherAppInstallation},
		installations: map[string]*time.Time{testOtherAppInstallation: nil},
	})
	source := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)

	require.NoError(t, h.reconcile(t, testAppInstallation))

	require.Equal(t, business.DatasourceStatusActive, h.reload(t, source.ID).Status,
		"a source the app still covers must keep syncing whatever the column said")
}

// GitHub being briefly unavailable must not look like revocation: the job fails
// and retries, and every source keeps its status meanwhile.
func TestReconcileInstallationOutageDoesNotParkSources(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{failStatus: http.StatusInternalServerError})
	source := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)

	require.Error(t, h.reconcile(t, testAppInstallation))

	require.Equal(t, business.DatasourceStatusActive, h.reload(t, source.ID).Status)
}

// One installation usually covers several sources; its state is read once, not
// once per source.
func TestReconcileInstallationReadsInstallationStateOncePerInstallation(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{
		coverage: map[string]string{
			"acme/docs":    testAppInstallation,
			"acme/widgets": testAppInstallation,
			"acme/specs":   testAppInstallation,
		},
		installations: map[string]*time.Time{testAppInstallation: nil},
	})
	h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)
	h.seedAppSource(t, "source-b", "acme/widgets", testAppInstallation)
	h.seedAppSource(t, "source-c", "acme/specs", testAppInstallation)

	require.NoError(t, h.reconcile(t, testAppInstallation))

	coverageReads, installationReads := h.api.reads()
	require.Equal(t, 3, coverageReads, "each source's repository is checked on its own")
	require.Equal(t, 1, installationReads, "the shared installation is read once")
}

// Sources bound to other installations are not in scope for this delivery.
func TestReconcileInstallationTouchesOnlyItsOwnSources(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{
		coverage:      map[string]string{"acme/widgets": testOtherAppInstallation},
		installations: map[string]*time.Time{testOtherAppInstallation: nil},
	})
	revoked := h.seedAppSource(t, "source-a", "acme/docs", testAppInstallation)
	other := h.seedAppSource(t, "source-b", "acme/widgets", testOtherAppInstallation)

	require.NoError(t, h.reconcile(t, testAppInstallation))

	require.Equal(t, business.DatasourceStatusDegraded, h.reload(t, revoked.ID).Status)
	require.Equal(t, business.DatasourceStatusActive, h.reload(t, other.ID).Status)
}

func TestInstallationJobHandlerRejectsMisroutedWork(t *testing.T) {
	h := newInstallationHarness(t, &fakeInstallationAPI{})
	handler := h.svc.NewDatasourceInstallationJobHandler()

	for name, envelope := range map[string]*jobsv1.JobEnvelope{
		"another queue": {Queue: "datasource.deliveries", Topic: "datasource.github.installation"},
		"another topic": {Queue: business.DatasourceInstallationQueue, Topic: "datasource.github.push"},
		"no installation": {
			Queue: business.DatasourceInstallationQueue,
			Topic: "datasource.github.installation",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := handler(context.Background(), envelope)
			require.Error(t, err)
			require.Contains(t, fmt.Sprint(err), "datasource.invalid_job")
		})
	}
}
