//go:build !race_only

package business_test

// The reconcile's coverage: what every provider is scheduled with at connect,
// where one occurrence sends each of them, and that two attempts of one
// occurrence do not do the work twice.
//
// Before the schedule covered every provider it was GitHub's alone, twice over:
// a pull source (api, crawler, upload) was inserted with no schedule at all,
// and the due-set query filtered provider = 'github' on top of that. None of
// those three has a webhook receiver either, so a connected source of them
// drifted from its source with nothing anywhere noticing, forever, until a
// person pressed "Sync now". The schedule is now declared rather than swept
// (datasource_trigger.go), and the coverage it must keep is the same.

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/sdk-go/receipts"
)

// jobsFor returns every recorded job naming source.
func jobsFor(p *recordingProducer, sourceID string) []*jobsv1.NewJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*jobsv1.NewJob
	for _, job := range p.jobs {
		if job.GetAttributes()["datasource.source_id"] == sourceID {
			out = append(out, job)
		}
	}
	return out
}

// A pull source is scheduled at connect, and it is scheduled a day out rather
// than at GitHub's half hour: each of these connectors re-sends its whole
// content on every sync, so the safety net is deliberately slack.
func TestAddSource_SchedulesEveryProviderAndSyncsAtConnect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input business.AddSourceInput
		every time.Duration
	}{
		{"crawler", business.AddSourceInput{
			OrgID: testOrg, Provider: business.DatasourceProviderCrawler,
			CollectionLabel: "guides", Crawler: crawlerConfig(),
		}, 24 * time.Hour},
		{"upload", business.AddSourceInput{
			OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides",
			Credential: "secretkey", Upload: uploadConfig(),
		}, 24 * time.Hour},
		{"api", business.AddSourceInput{
			OrgID: testOrg, Provider: business.DatasourceProviderAPI, CollectionLabel: "guides",
			Credential: "sekret", API: apiConfig(),
		}, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			producer := &recordingProducer{}
			store := newDatasourceFakeStore()
			svc, _ := newDatasourceService(store, producer, nil)

			source, err := svc.AddExistingSource(context.Background(), "actor-1", tc.input)
			if err != nil {
				t.Fatal(err)
			}
			stored := storedSource(t, store, source.ID)
			if stored.ReconcileInterval != tc.every {
				t.Fatalf("reconcile interval = %v, want %v", stored.ReconcileInterval, tc.every)
			}
			if stored.NextReconcileAt == nil {
				t.Fatal("a connected source with no next_reconcile_at is outside the sweep for good")
			}

			// And it syncs now, not in a day: the schedule is the safety net, not
			// the first delivery. Before this, only a GitHub connect enqueued one.
			jobs := jobsFor(producer, source.ID)
			if len(jobs) != 1 || jobs[0].GetQueue() != business.DatasourceSyncRequestQueue {
				t.Fatalf("connect enqueued %d job(s) for the source, want one sync request", len(jobs))
			}
		})
	}
}

// The declared trigger's occurrence routes every provider to the engine that
// provider actually syncs on: GitHub is compiled off the delivery queue,
// everything else is pulled by the sync worker. This used to be the sweep's
// coverage; the routing is unchanged and the caller is not.
func TestReconcileSourceOperation_RoutesEachProviderToItsEngine(t *testing.T) {
	producer := &recordingProducer{}
	store := newReconcileOperationStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	gh := githubSource(t, svc, "main", nil, "")
	crawler := newCrawlerSource(t, svc)
	takeConnectSync(t, producer, crawler)

	reconcileOnce(t, svc, gh.OrgID, gh.ID, "effect-github")
	reconcileOnce(t, svc, crawler.OrgID, crawler.ID, "effect-crawler")

	ghJobs := jobsFor(producer, gh.ID)
	if len(ghJobs) != 1 || ghJobs[0].GetQueue() != business.DatasourceDeliveryQueue ||
		ghJobs[0].GetTopic() != business.DatasourceReconcileTopic {
		t.Fatalf("github routing = %+v", ghJobs)
	}
	if mode := ghJobs[0].GetAttributes()["datasource.reconcile_mode"]; mode != "conditional" {
		t.Fatalf("a reconcile pass must be conditional, got %q", mode)
	}
	// The job is a message on exactly the same terms as a manual "Sync now":
	// the platform validates content_type (min_len 1) on every enqueue, and an
	// operation whose job is refused must fail the occurrence rather than
	// report a pass that enqueued nothing.
	if ghJobs[0].GetContentType() != "application/json" || string(ghJobs[0].GetPayload()) != "{}" {
		t.Fatalf("reconcile body = %q/%q, want an empty JSON object",
			ghJobs[0].GetContentType(), ghJobs[0].GetPayload())
	}
	if ghJobs[0].GetSchemaVersion() != 1 {
		t.Fatalf("schema version = %d, want the reconcile request's (1), not the change set's (2)", ghJobs[0].GetSchemaVersion())
	}

	pullJobs := jobsFor(producer, crawler.ID)
	if len(pullJobs) != 1 || pullJobs[0].GetQueue() != business.DatasourceSyncRequestQueue {
		t.Fatalf("pull routing = %+v", pullJobs)
	}
	// Provenance separates a declared schedule's pass from a person's "Sync
	// now"; both are performed by the same worker, so nothing else
	// distinguishes them.
	if src := pullJobs[0].GetSource(); src != business.DatasourceScheduledSyncSource {
		t.Fatalf("reconcile pull provenance = %q, want %q", src, business.DatasourceScheduledSyncSource)
	}
	// And it carries the per-source FIFO key, so it cannot run beside a "Sync
	// now" re-sending the same whole source.
	if ordering := pullJobs[0].GetOrdering(); ordering.GetNamespace() != "datasource.sync" ||
		len(ordering.GetComponents()) != 1 || ordering.GetComponents()[0] != crawler.ID {
		t.Fatalf("reconcile pull ordering key = %+v", ordering)
	}
}

// Two attempts of one occurrence are one job. The occurrence's effect identity
// is what makes that true, and it replaces the schedule-derived key the sweep
// needed while two replicas could both select one due row: for a pull provider
// a duplicate is a second full re-send of the whole source, with no cursor to
// make the second one cheap.
//
// The recording producer deliberately does not implement idempotency — the
// platform does — so the test reads the keys the attempts produced rather than
// counting jobs it would have collapsed.
func TestReconcileSourceOperation_TwoAttemptsOfOneOccurrenceAreOneJob(t *testing.T) {
	producer := &recordingProducer{}
	store := newReconcileOperationStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	source := newUploadSource(t, svc)
	takeConnectSync(t, producer, source)

	reconcileOnce(t, svc, source.OrgID, source.ID, "occurrence-1")
	reconcileOnce(t, svc, source.OrgID, source.ID, "occurrence-1")
	keys := jobKeys(producer, source.ID)
	if len(keys) != 2 {
		t.Fatalf("expected one job per attempt in the recording, got %d", len(keys))
	}
	if keys[0] != keys[1] {
		t.Fatalf("two attempts of one occurrence produced different idempotency keys (%q, %q); the platform would run the pull twice",
			keys[0], keys[1])
	}

	// The next occurrence is different work and must not be swallowed as a
	// replay of the one before it.
	reconcileOnce(t, svc, source.OrgID, source.ID, "occurrence-2")
	later := jobKeys(producer, source.ID)
	if len(later) != 3 || later[2] == keys[0] {
		t.Fatalf("the next occurrence reused the previous one's key %q", later[2])
	}
}

// NOTHING IS ENQUEUED FOR A PASS THAT WILL BE REFUSED (issue #1121, m2/N4).
//
// The effect's binding and digest are read before the enqueue, because both
// refusals are decisions about the attempt row alone. Enqueueing and then
// refusing left a job behind for a request this host rejected — and since the
// key names the source, a re-presented identity naming a DIFFERENT source
// produced a key of its own: a real second re-send of that source rather than
// a collapsed replay.
func TestReconcileSourceOperation_RefusedPassEnqueuesNothing(t *testing.T) {
	producer := &recordingProducer{}
	store := newReconcileOperationStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	first := newUploadSource(t, svc)
	second := newCrawlerSource(t, svc)
	takeConnectSync(t, producer, first)
	takeConnectSync(t, producer, second)

	// One occurrence of the first source, admitted and recorded.
	reconcileOnce(t, svc, first.OrgID, first.ID, "occurrence-1")
	if got := len(jobKeys(producer, first.ID)); got != 1 {
		t.Fatalf("the admitted pass enqueued %d job(s), want 1", got)
	}

	// The same identity, now naming the second source. It is refused — and the
	// refusal must happen before anything reaches the queue.
	digest := sha256.Sum256([]byte("occurrence-1"))
	ctx := receipts.WithEffect(t.Context(), receipts.Effect{
		ID: "occurrence-1", Tenant: second.OrgID, Method: business.ReconcileSourceMethod, RequestDigest: digest[:],
	})
	err := svc.ReconcileDatasourceSource(ctx, "actor", second.OrgID, second.ID,
		func(context.Context, string) error { return nil })
	if err == nil {
		t.Fatal("an effect identity bound to another source was admitted")
	}
	if jobs := jobKeys(producer, second.ID); len(jobs) != 0 {
		t.Fatalf("a refused pass enqueued %d job(s) for the second source: %v", len(jobs), jobs)
	}

	// And the same source with a different request digest: refused as a reuse,
	// again with nothing enqueued beyond the one admitted pass.
	other := sha256.Sum256([]byte("a different request"))
	ctx = receipts.WithEffect(t.Context(), receipts.Effect{
		ID: "occurrence-1", Tenant: first.OrgID, Method: business.ReconcileSourceMethod, RequestDigest: other[:],
	})
	if err := svc.ReconcileDatasourceSource(ctx, "actor", first.OrgID, first.ID,
		func(context.Context, string) error { return nil }); err == nil {
		t.Fatal("an effect identity re-presented with another request was admitted")
	}
	if got := len(jobKeys(producer, first.ID)); got != 1 {
		t.Fatalf("a refused reuse enqueued a second job: %d", got)
	}
}

// reconcileOnce runs one reconcile pass as one occurrence of the declared
// trigger would: an effect identity in context, and the receipt committed by
// the callback the operation hands it.
func reconcileOnce(t *testing.T, svc *business.Service, orgID, sourceID, effect string) {
	t.Helper()
	digest := sha256.Sum256([]byte(effect))
	ctx := receipts.WithEffect(t.Context(), receipts.Effect{
		ID: effect, Tenant: orgID, Method: business.ReconcileSourceMethod, RequestDigest: digest[:],
	})
	if err := svc.ReconcileDatasourceSource(ctx, "actor", orgID, sourceID, func(context.Context, string) error { return nil }); err != nil {
		t.Fatalf("reconcile %s: %v", effect, err)
	}
}

func jobKeys(p *recordingProducer, sourceID string) []string {
	var keys []string
	for _, job := range jobsFor(p, sourceID) {
		keys = append(keys, job.GetIdempotencyKey())
	}
	return keys
}

// reconcileOperationStore is the fake store the operation needs beyond the
// datasource one: the membership it authorizes against and the durable attempt
// row that binds one effect identity to one actor and source.
type reconcileOperationStore struct {
	*datasourceFakeStore
	attempts map[string]business.SourceOperationAttempt
}

func newReconcileOperationStore() *reconcileOperationStore {
	return &reconcileOperationStore{datasourceFakeStore: newDatasourceFakeStore(), attempts: map[string]business.SourceOperationAttempt{}}
}

func (s *reconcileOperationStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	return &gen.OrgMembership{Role: gen.OrgRole_ORG_ROLE_ADMIN}, nil
}

// The attempt row is keyed by (ORG, effect), as the real table is
// (`datasource_operation_attempts` has a composite primary key on
// `org_id, effect_id`). Keying the fake by the effect alone made one
// organization's attempt row answer another's lookup — a cross-tenant
// collision the real store cannot have, which would have refused a legitimate
// call and masked the enqueue-key scoping this package tests.
func attemptKey(org, effect string) string { return org + "\x00" + effect }

func (s *reconcileOperationStore) GetSourceOperationAttempt(_ context.Context, org, id string) (*business.SourceOperationAttempt, error) {
	attempt, ok := s.attempts[attemptKey(org, id)]
	if !ok {
		return nil, nil
	}
	return &attempt, nil
}

func (s *reconcileOperationStore) CreateSourceOperationAttempt(_ context.Context, attempt business.SourceOperationAttempt) (bool, error) {
	key := attemptKey(attempt.OrgID, attempt.EffectID)
	if _, ok := s.attempts[key]; ok {
		return false, nil
	}
	s.attempts[key] = attempt
	return true, nil
}

func (s *reconcileOperationStore) DeleteSourceOperationAttempt(_ context.Context, org, id string) error {
	delete(s.attempts, attemptKey(org, id))
	return nil
}
