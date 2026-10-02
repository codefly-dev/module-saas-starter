//go:build !race_only

package business_test

// The periodic reconcile's coverage: which sources it selects, where it sends
// each of them, and that sweeping the same due row twice does not do the work
// twice.
//
// Before this, the sweep was GitHub's alone — twice over. A pull source (api,
// crawler, upload) was inserted with no next_reconcile_at, and the due-set query
// filtered provider = 'github' on top of that. None of those three has a webhook
// receiver either, so a connected source of them drifted from its source with
// nothing anywhere noticing, forever, until a person pressed "Sync now".

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
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

// The sweep selects every provider and routes each to the engine that provider
// actually syncs on: GitHub is compiled off the delivery queue, everything else
// is pulled by the sync worker.
func TestRunDatasourceReconcile_RoutesEachProviderToItsEngine(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	gh := githubSource(t, svc, "main", nil, "")
	crawler := newCrawlerSource(t, svc)
	takeConnectSync(t, producer, crawler)
	setNextReconcile(t, store, gh.ID, time.Now().Add(-time.Minute))
	setNextReconcile(t, store, crawler.ID, time.Now().Add(-time.Minute))

	n, err := svc.RunDatasourceReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("swept %d sources, want both the GitHub source and the pull source", n)
	}

	ghJobs := jobsFor(producer, gh.ID)
	if len(ghJobs) != 1 || ghJobs[0].GetQueue() != business.DatasourceDeliveryQueue ||
		ghJobs[0].GetTopic() != business.DatasourceReconcileTopic {
		t.Fatalf("github routing = %+v", ghJobs)
	}
	if mode := ghJobs[0].GetAttributes()["datasource.reconcile_mode"]; mode != "conditional" {
		t.Fatalf("the periodic pass must be conditional, got %q", mode)
	}

	pullJobs := jobsFor(producer, crawler.ID)
	if len(pullJobs) != 1 || pullJobs[0].GetQueue() != business.DatasourceSyncRequestQueue {
		t.Fatalf("pull routing = %+v", pullJobs)
	}
	// Provenance separates the schedule's pass from a person's "Sync now"; both
	// are performed by the same worker, so nothing else distinguishes them.
	if src := pullJobs[0].GetSource(); src != business.DatasourceScheduledSyncSource {
		t.Fatalf("scheduled pull provenance = %q, want %q", src, business.DatasourceScheduledSyncSource)
	}
	// And it carries the per-source FIFO key, so it cannot run beside a "Sync
	// now" re-sending the same whole source.
	if ordering := pullJobs[0].GetOrdering(); ordering.GetNamespace() != "datasource.sync" ||
		len(ordering.GetComponents()) != 1 || ordering.GetComponents()[0] != crawler.ID {
		t.Fatalf("scheduled pull ordering key = %+v", ordering)
	}
}

// The sweep holds no lease and runs in every replica. Two replicas that select
// the same due row before either reschedules it must enqueue the work once, not
// once each: for a pull provider a duplicate is a second full re-send of the
// whole source, with no cursor to make the second one cheap.
//
// What makes that true is the idempotency key, which is what this asserts. The
// recording producer deliberately does not implement idempotency — the platform
// does — so the test reads the keys the two sweeps produced rather than counting
// jobs it would have collapsed.
func TestRunDatasourceReconcile_TwoSweepsOfOneDueRowAreOneJob(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	source := newUploadSource(t, svc)
	takeConnectSync(t, producer, source)
	due := time.Now().Add(-time.Minute)
	setNextReconcile(t, store, source.ID, due)

	first := jobKeysAfterSweep(t, svc, producer, source.ID)
	// The second replica read the row before the first rescheduled it, so it sees
	// the same schedule instant and must derive the same key.
	setNextReconcile(t, store, source.ID, due)
	second := jobKeysAfterSweep(t, svc, producer, source.ID)

	if len(first) != 1 || len(second) != 2 {
		t.Fatalf("expected one job per sweep in the recording, got %d then %d", len(first), len(second))
	}
	if first[0] != second[1] {
		t.Fatalf("two sweeps of one due schedule produced different idempotency keys (%q, %q); the platform would run the pull twice",
			first[0], second[1])
	}

	// A later schedule is different work and must not be swallowed as a replay of
	// the pass before it. (Set in the past so the row is due again; what the key
	// distinguishes is the schedule instant, not how recent it is.)
	setNextReconcile(t, store, source.ID, due.Add(-time.Hour))
	later := jobKeysAfterSweep(t, svc, producer, source.ID)
	if len(later) != 3 || later[2] == first[0] {
		t.Fatalf("the next scheduled pass reused the previous pass's key %q", later[2])
	}
}

func jobKeysAfterSweep(t *testing.T, svc *business.Service, p *recordingProducer, sourceID string) []string {
	t.Helper()
	if _, err := svc.RunDatasourceReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, job := range jobsFor(p, sourceID) {
		keys = append(keys, job.GetIdempotencyKey())
	}
	return keys
}
