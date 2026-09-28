package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

// The sync job id has to reach whichever module admits work for a hand-off, or
// that module cannot tie its own records back to the run a person is looking
// at. The trap this guards is a PARTIAL stamp: the reconcile path already had
// the id in hand for its idempotency key, while the push path passed nothing
// and the per-file path had no parameter for it at all. A value present on a
// reconcile and absent on a webhook delivery is worse than one absent
// everywhere, because the correlated view would then be silently incomplete
// for some syncs and correct for others -- and the small, obvious test case
// (one reconcile, one file) is the one that passes either way.
func TestHandoffJobsCarryTheSyncJobID(t *testing.T) {
	const jobID = "11111111-1111-1111-1111-111111111111"

	// An incremental push: the source has a cursor, so the compiler compares and
	// hands off ONE JOB PER CHANGED FILE. This is the path that had no parameter
	// for the sync job id at all, and the one a snapshot-shaped test silently
	// fails to cover -- a source with no cursor snapshots instead, and the
	// per-file stamp can then be deleted with every test still green.
	t.Run("webhook delivery, one job per changed file", func(t *testing.T) {
		producer := &recordingProducer{}
		gh := &fakeGitHub{
			commit:  cB,
			content: map[string][]byte{"docs/a.md": []byte("A"), "docs/b.md": []byte("B")},
			compareFn: compareBetween(cA, cB, []github.ChangedFile{
				{Filename: "docs/a.md", Status: "modified", SHA: "sa"},
				{Filename: "docs/b.md", Status: "added", SHA: "sb"},
			}),
		}
		store := newDatasourceFakeStore()
		svc, _ := newDatasourceService(store, producer, gh)
		source := githubSource(t, svc, "main", []string{"docs"}, cA)
		// githubSource sets the cursor on the value it returns, not in the store.
		// The handler loads the source by id, so without this the source it sees
		// has no cursor and SNAPSHOTS -- which is how an "incremental" test comes
		// to assert nothing about the per-file path.
		store.mu.Lock()
		store.sources[source.ID].LastIngestedCommit = cA
		store.mu.Unlock()

		handler := svc.NewDatasourceDeliveryJobHandler()
		if err := handler(context.Background(), &jobsv1.JobEnvelope{
			Id:         jobID,
			Queue:      business.DatasourceDeliveryQueue,
			Topic:      "datasource.github.push",
			Attributes: map[string]string{"datasource.source_id": source.ID},
			Payload:    pushDelivery("refs/heads/main", cA, cB, false, false),
		}); err != nil {
			t.Fatal(err)
		}

		// Two changed files must be two hand-offs, or this is a snapshot wearing
		// an incremental label and it proves nothing about the per-file path.
		if len(producer.jobs) != 2 {
			t.Fatalf("enqueued %d hand-offs, want one per changed file", len(producer.jobs))
		}
		assertEveryHandoffNamesTheSync(t, producer, jobID)
	})

	// A reconcile whose head has moved past the cursor: one snapshot hand-off,
	// which is the other shape -- one job covering many files rather than one
	// job per file.
	t.Run("reconcile, a single snapshot hand-off", func(t *testing.T) {
		producer := &recordingProducer{}
		gh := &fakeGitHub{commit: "HEAD", files: []github.File{
			{Path: "docs/a.md", SHA: "sa"},
			{Path: "docs/b.md", SHA: "sb"},
		}}
		svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
		source := githubSource(t, svc, "main", []string{"docs"}, "older")

		handler := svc.NewDatasourceDeliveryJobHandler()
		if err := handler(context.Background(), &jobsv1.JobEnvelope{
			Id:         jobID,
			Queue:      business.DatasourceDeliveryQueue,
			Topic:      business.DatasourceReconcileTopic,
			Attributes: map[string]string{"datasource.source_id": source.ID},
		}); err != nil {
			t.Fatal(err)
		}

		assertEveryHandoffNamesTheSync(t, producer, jobID)
	})
}

// A hand-off that arrives outside the delivery queue names no sync, rather than
// naming the empty one: a consumer can then tell "this hand-off belongs to no
// run I can look up" from "this hand-off belongs to a run whose id is blank".
func TestDirectCompileStampsNoSyncJobID(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, &fakeGitHub{})
	source := githubSource(t, svc, "main", nil, "")

	if _, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", cA, cB, false, false), "d1"); err != nil {
		t.Fatal(err)
	}
	if len(producer.jobs) == 0 {
		t.Fatal("no hand-off jobs enqueued; the test proves nothing")
	}
	for _, job := range producer.jobs {
		if _, stamped := job.GetAttributes()["datasource.sync.job_id"]; stamped {
			t.Fatalf("hand-off for %q names a sync it did not arrive under",
				job.GetAttributes()["github.path"])
		}
	}
}

func assertEveryHandoffNamesTheSync(t *testing.T, producer *recordingProducer, jobID string) {
	t.Helper()
	if len(producer.jobs) == 0 {
		t.Fatal("no hand-off jobs enqueued; the assertion below would vacuously pass")
	}
	for _, job := range producer.jobs {
		got := job.GetAttributes()["datasource.sync.job_id"]
		if got != jobID {
			t.Errorf("hand-off %q on topic %q carries sync job id %q, want %q",
				job.GetAttributes()["github.path"], job.GetTopic(), got, jobID)
		}
	}
}
