package business_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/datasource"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
)

const (
	testPushInstallation      = "4242"
	testOtherPushInstallation = "9999"
	testPushRepo              = "acme/docs"
	testPushDelivery          = "delivery-push-1"
	testSecondOrg             = "22222222-2222-2222-2222-222222222222"
)

// appPushDeliveryBody is a push as the App receiver verified and retained it.
// The fan-out never reads it — it only hands the bytes on — so its shape
// matters here only as something to compare against verbatim.
const appPushDeliveryBody = `{"ref":"refs/heads/main","before":"aaa","after":"bbb",` +
	`"installation":{"id":4242},"repository":{"full_name":"acme/docs"}}`

// appPushProducer records the per-source deliveries a fan-out produces and can
// fail a chosen source's enqueue, which is how a partial fan-out is reproduced.
type appPushProducer struct {
	mu      sync.Mutex
	jobs    []*jobsv1.NewJob
	failFor map[string]error
}

func (p *appPushProducer) EnqueueJob(_ context.Context, request *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	// Enforce the same command contract the real producer does, so a job this
	// path builds that violates saas.jobs.v1 fails here rather than in
	// production.
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}
	job := request.GetJob()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err, ok := p.failFor[job.GetAttributes()["datasource.source_id"]]; ok {
		return nil, err
	}
	p.jobs = append(p.jobs, job)
	return &jobsv1.EnqueueJobResponse{
		JobId:       "job",
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

func (p *appPushProducer) recorded() []*jobsv1.NewJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*jobsv1.NewJob(nil), p.jobs...)
}

func (p *appPushProducer) keys() []string {
	var keys []string
	for _, job := range p.recorded() {
		keys = append(keys, job.GetIdempotencyKey())
	}
	return keys
}

// newAppPushService wires only what the fan-out uses: the source store and the
// job producer. It needs no GitHub client and no audit emitter, which is itself
// the point — resolving who a push belongs to is a store read, not a fetch.
func newAppPushService(store *datasourceFakeStore, producer *appPushProducer) *business.Service {
	svc, _ := business.NewService(store)
	svc.SetDatasourceConnector(purposeCipher{}, producer, "")
	return svc
}

func seedPushSource(t *testing.T, store *datasourceFakeStore, id, org, repo, installation, status string) *business.DatasourceSource {
	t.Helper()
	source := &business.DatasourceSource{
		ID:                   id,
		OrgID:                org,
		Provider:             business.DatasourceProviderGitHub,
		Repo:                 repo,
		Branch:               "main",
		BoundaryNodeID:       "boundary-" + id,
		CredentialSecretRef:  "envelope-" + id,
		Status:               status,
		GitHubInstallationID: installation,
	}
	require.NoError(t, store.InsertDatasourceSource(context.Background(), source))
	return source
}

// One installation can serve the same repository in several tenants, and the
// delivery names none of them. Every eligible source gets its own delivery,
// stamped with the tenant and boundary from its own row — never a single tenant
// inferred from the payload.
func TestFanOutGitHubAppPushReachesEveryEligibleTenant(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{}
	svc := newAppPushService(store, producer)

	first := seedPushSource(t, store, "source-a", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)
	second := seedPushSource(t, store, "source-b", testSecondOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)

	fanned, err := svc.FanOutGitHubAppPush(context.Background(),
		testPushInstallation, testPushRepo, testPushDelivery, []byte(appPushDeliveryBody))
	require.NoError(t, err)
	require.Equal(t, 2, fanned)

	recorded := producer.recorded()
	require.Len(t, recorded, 2)

	bySource := map[string]*jobsv1.NewJob{}
	for _, job := range recorded {
		bySource[job.GetAttributes()["datasource.source_id"]] = job
	}
	for _, source := range []*business.DatasourceSource{first, second} {
		job, ok := bySource[source.ID]
		require.True(t, ok, source.ID)

		// An ordinary per-source delivery on the existing queue and topic, so
		// the change-set compiler handles it with no knowledge of where it
		// entered.
		require.Equal(t, business.DatasourceDeliveryQueue, job.GetQueue())
		require.Equal(t, appPushDeliveryBody, string(job.GetPayload()))

		// The fan-out enqueues onto the receiver's own push topic, so it must
		// stamp the receiver's topic and schema version: one consumer must not
		// see two different envelopes depending on which producer sent it. The
		// production constants cannot be shared — pkg/datasource imports
		// business, so business importing it back is a cycle — but this external
		// test package can import both, which is what couples them here.
		require.Equal(t, datasource.GitHubWebhookTopic, job.GetTopic())
		require.EqualValues(t, datasource.GitHubWebhookSchemaVersion, job.GetSchemaVersion())

		// Tenant and boundary are bound server-side from the source row.
		require.Equal(t, source.OrgID, job.GetAttributes()["datasource.org_id"])
		require.Equal(t, source.BoundaryNodeID, job.GetAttributes()["datasource.boundary_id"])
		require.Equal(t, testPushDelivery, job.GetAttributes()["datasource.delivery_id"])

		// Per-source FIFO, so this source's deliveries never race each other.
		require.Equal(t, []string{source.ID}, job.GetOrdering().GetComponents())

		// Stable original identity paired with stable per-source identity.
		require.Equal(t, business.GitHubAppPushDeliveryKey(testPushDelivery, source.ID), job.GetIdempotencyKey())
	}
	require.NotEqual(t, recorded[0].GetIdempotencyKey(), recorded[1].GetIdempotencyKey(),
		"one delivery must not collapse two sources into one job")
}

// Eligibility is re-read on every delivery. A suspended installation and a
// deselected repository both leave the source parked, an operator pause leaves
// it paused, and neither a different repository, a different installation nor
// another provider is this delivery's business.
func TestFanOutGitHubAppPushSkipsIneligibleSources(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{}
	svc := newAppPushService(store, producer)

	seedPushSource(t, store, "parked", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusDegraded)
	seedPushSource(t, store, "paused", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusPaused)
	seedPushSource(t, store, "other-repo", testOrg, "acme/specs", testPushInstallation, business.DatasourceStatusActive)
	seedPushSource(t, store, "other-installation", testOrg, testPushRepo, testOtherPushInstallation, business.DatasourceStatusActive)

	notGitHub := seedPushSource(t, store, "not-github", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)
	notGitHub.Provider = business.DatasourceProviderAPI
	require.NoError(t, store.InsertDatasourceSource(context.Background(), notGitHub))

	fanned, err := svc.FanOutGitHubAppPush(context.Background(),
		testPushInstallation, testPushRepo, testPushDelivery, []byte(appPushDeliveryBody))
	require.NoError(t, err)
	require.Zero(t, fanned)
	require.Empty(t, producer.recorded(), "a revoked or unrelated source must produce no delivery")
}

// GitHub's repository names are case-insensitive: a delivery carries the
// repository's current casing while the row carries the casing it was connected
// with, and a case difference between them is the same repository.
func TestFanOutGitHubAppPushMatchesRepositoryCaseInsensitively(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{}
	svc := newAppPushService(store, producer)

	seedPushSource(t, store, "source-a", testOrg, "Acme/Docs", testPushInstallation, business.DatasourceStatusActive)

	fanned, err := svc.FanOutGitHubAppPush(context.Background(),
		testPushInstallation, "acme/docs", testPushDelivery, []byte(appPushDeliveryBody))
	require.NoError(t, err)
	require.Equal(t, 1, fanned)
}

// A redelivered push re-derives the same per-source keys, so the jobs platform
// resolves each to the job already recorded rather than compiling the change
// twice.
func TestFanOutGitHubAppPushRedeliveryReusesPerSourceKeys(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{}
	svc := newAppPushService(store, producer)

	seedPushSource(t, store, "source-a", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)
	seedPushSource(t, store, "source-b", testSecondOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)

	for range 2 {
		_, err := svc.FanOutGitHubAppPush(context.Background(),
			testPushInstallation, testPushRepo, testPushDelivery, []byte(appPushDeliveryBody))
		require.NoError(t, err)
	}
	require.ElementsMatch(t,
		[]string{
			business.GitHubAppPushDeliveryKey(testPushDelivery, "source-a"),
			business.GitHubAppPushDeliveryKey(testPushDelivery, "source-b"),
			business.GitHubAppPushDeliveryKey(testPushDelivery, "source-a"),
			business.GitHubAppPushDeliveryKey(testPushDelivery, "source-b"),
		},
		producer.keys(),
		"a redelivery must re-derive the same keys, which is what makes it a duplicate")
}

// A fan-out that fails partway must not strand the sources it had not reached,
// which may belong to other tenants. It reports the failure so the delivery is
// retried, and the retry re-walks the installation: what already landed keeps
// its key and is resolved as a duplicate, what did not is delivered.
func TestFanOutGitHubAppPushRecoversAfterPartialFailure(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{failFor: map[string]error{"source-b": errors.New("inbox unavailable")}}
	svc := newAppPushService(store, producer)

	seedPushSource(t, store, "source-a", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)
	seedPushSource(t, store, "source-b", testSecondOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)
	seedPushSource(t, store, "source-c", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)

	fanned, err := svc.FanOutGitHubAppPush(context.Background(),
		testPushInstallation, testPushRepo, testPushDelivery, []byte(appPushDeliveryBody))
	require.Error(t, err, "a partial fan-out must be reported so the delivery is retried")
	require.Equal(t, 2, fanned)
	require.ElementsMatch(t,
		[]string{
			business.GitHubAppPushDeliveryKey(testPushDelivery, "source-a"),
			business.GitHubAppPushDeliveryKey(testPushDelivery, "source-c"),
		},
		producer.keys(),
		"one source's failure must not strand the sources behind it")

	producer.mu.Lock()
	producer.failFor = nil
	producer.mu.Unlock()

	fanned, err = svc.FanOutGitHubAppPush(context.Background(),
		testPushInstallation, testPushRepo, testPushDelivery, []byte(appPushDeliveryBody))
	require.NoError(t, err)
	require.Equal(t, 3, fanned)
	require.Contains(t, producer.keys(), business.GitHubAppPushDeliveryKey(testPushDelivery, "source-b"),
		"the retry must deliver the source the first attempt missed")
}

// One installation can hold more sources for a repository than a single read
// returns, so the fan-out pages. A source on a later page must not be dropped.
func TestFanOutGitHubAppPushPagesEveryEligibleSource(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{}
	svc := newAppPushService(store, producer)

	const seeded = 150 // more than one page
	for i := range seeded {
		seedPushSource(t, store, fmt.Sprintf("source-%04d", i), testOrg, testPushRepo, testPushInstallation,
			business.DatasourceStatusActive)
	}

	fanned, err := svc.FanOutGitHubAppPush(context.Background(),
		testPushInstallation, testPushRepo, testPushDelivery, []byte(appPushDeliveryBody))
	require.NoError(t, err)
	require.Equal(t, seeded, fanned)

	distinct := map[string]bool{}
	for _, key := range producer.keys() {
		distinct[key] = true
	}
	require.Len(t, distinct, seeded, "every source gets exactly one delivery of its own")
}

// The App content delivery is dispatched to the fan-out before the per-source
// lookup the other topics take, because it names an installation and repository
// rather than a source.
func TestDatasourceDeliveryJobRoutesAppPushToFanOut(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{}
	svc := newAppPushService(store, producer)
	seedPushSource(t, store, "source-a", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)

	err := svc.NewDatasourceDeliveryJobHandler()(context.Background(), &jobsv1.JobEnvelope{
		Queue: business.DatasourceDeliveryQueue,
		Topic: datasource.GitHubAppPushTopic,
		Attributes: map[string]string{
			"datasource.installation_id": testPushInstallation,
			"github.repo":                testPushRepo,
			"datasource.delivery_id":     testPushDelivery,
		},
		Payload: []byte(appPushDeliveryBody),
	})
	require.NoError(t, err)
	require.Equal(t, []string{business.GitHubAppPushDeliveryKey(testPushDelivery, "source-a")}, producer.keys())
}

// A job carrying neither routing fact cannot be fanned out and will not carry
// them on redelivery either, so it is terminal rather than retried forever.
func TestDatasourceDeliveryJobRejectsUnroutableAppPush(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{}
	svc := newAppPushService(store, producer)

	for name, attributes := range map[string]map[string]string{
		"no repository":   {"datasource.installation_id": testPushInstallation},
		"no installation": {"github.repo": testPushRepo},
	} {
		err := svc.NewDatasourceDeliveryJobHandler()(context.Background(), &jobsv1.JobEnvelope{
			Queue:      business.DatasourceDeliveryQueue,
			Topic:      "datasource.github.app_push",
			Attributes: attributes,
			Payload:    []byte(appPushDeliveryBody),
		})
		var processing *jobs.ProcessingError
		require.ErrorAs(t, err, &processing, name)
		require.False(t, processing.Retryable, name)
	}
	require.Empty(t, producer.recorded())
}

// A job the platform refused cannot become valid by being sent again. Left as a
// bare error it inherited the worker's default retryable classification and
// burned all 24 attempts re-sending it, so the fan-out names it terminal.
func TestFanOutGitHubAppPushTreatsRefusedCommandAsTerminal(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{failFor: map[string]error{
		"source-a": fmt.Errorf("%w: ordering component too long", jobs.ErrInvalidCommand),
	}}
	svc := newAppPushService(store, producer)
	seedPushSource(t, store, "source-a", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)

	fanned, err := svc.FanOutGitHubAppPush(context.Background(),
		testPushInstallation, testPushRepo, testPushDelivery, []byte(appPushDeliveryBody))
	require.Zero(t, fanned)
	var processing *jobs.ProcessingError
	require.ErrorAs(t, err, &processing)
	require.False(t, processing.Retryable, "a refused command must not be retried")
}

// One source's terminal refusal must not decide the whole delivery: a transient
// failure on any other source has to keep it alive, or the sources whose
// enqueue merely needed retrying are silently dropped when the delivery
// dead-letters. This is why both branches are classified, not just the terminal
// one — keepRetryable only prefers a retryable *ProcessingError*.
func TestFanOutGitHubAppPushKeepsDeliveryAliveForTransientFailure(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &appPushProducer{failFor: map[string]error{
		"source-a": fmt.Errorf("%w: refused", jobs.ErrInvalidCommand),
		"source-b": errors.New("inbox unavailable"),
	}}
	svc := newAppPushService(store, producer)
	seedPushSource(t, store, "source-a", testOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)
	seedPushSource(t, store, "source-b", testSecondOrg, testPushRepo, testPushInstallation, business.DatasourceStatusActive)

	_, err := svc.FanOutGitHubAppPush(context.Background(),
		testPushInstallation, testPushRepo, testPushDelivery, []byte(appPushDeliveryBody))
	var processing *jobs.ProcessingError
	require.ErrorAs(t, err, &processing)
	require.True(t, processing.Retryable,
		"the transient failure must outrank the refusal so the delivery comes back")
}
