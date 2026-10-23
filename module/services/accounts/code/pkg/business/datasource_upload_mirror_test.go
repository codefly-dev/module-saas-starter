package business_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/objectstore"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
)

type mirrorListing struct {
	Bucket  string `json:"bucket"`
	Prefix  string `json:"prefix"`
	Ordinal int64  `json:"ordinal"`
	Files   *[]struct {
		Key  string `json:"key"`
		ETag string `json:"etag"`
	} `json:"files"`
	job *jobsv1.NewJob
}

// uploadListings decodes every listing job the producer received, in order.
func uploadListings(t *testing.T, p *recordingProducer) []mirrorListing {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []mirrorListing
	for _, job := range p.jobs {
		if job.GetTopic() != "datasource.upload.snapshot" {
			continue
		}
		var l mirrorListing
		if err := json.Unmarshal(job.GetPayload(), &l); err != nil {
			t.Fatalf("listing payload: %v", err)
		}
		l.job = job
		out = append(out, l)
	}
	return out
}

func uploadObjectJobs(p *recordingProducer) []*jobsv1.NewJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*jobsv1.NewJob
	for _, job := range p.jobs {
		if job.GetTopic() == "datasource.upload.sync" {
			out = append(out, job)
		}
	}
	return out
}

func mirrorBucket() *fakeUploadBucket {
	return &fakeUploadBucket{objects: []fakeObject{
		{key: "loans/a.xlsx", etag: "get-a", body: "A"}, {key: "loans/", etag: "dir"}, {key: "loans/b.xlsx", etag: "get-b", body: "B"},
	}}
}

// TestRunDatasourceSync_UploadClosesWithTheCompleteListing pins the mirror: a
// sync that saw the whole folder sends, after every object job, one listing of
// exactly the objects it delivered, versioned the way their jobs were, under
// the one ordinal all of the sync's jobs carry.
func TestRunDatasourceSync_UploadClosesWithTheCompleteListing(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	useUploadBucket(svc, mirrorBucket())
	source := newUploadSource(t, svc)

	if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	objects := uploadObjectJobs(producer)
	if len(objects) != 2 {
		t.Fatalf("object jobs = %d, want 2 (the folder marker is not a file)", len(objects))
	}
	listings := uploadListings(t, producer)
	if len(listings) != 1 {
		t.Fatalf("listings = %d, want 1", len(listings))
	}
	l := listings[0]
	if last := producer.jobs[len(producer.jobs)-1]; last != l.job {
		t.Fatal("the listing was not the last job of the sync")
	}
	if l.Files == nil {
		t.Fatal("the listing carries no files array")
	}
	got := map[string]string{}
	for _, f := range *l.Files {
		got[f.Key] = f.ETag
	}
	if want := map[string]string{"loans/a.xlsx": "get-a", "loans/b.xlsx": "get-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("listing files = %v, want %v (each object's delivered fingerprint)", got, want)
	}
	for _, job := range objects {
		if job.GetAttributes()["upload.etag"] != got[job.GetAttributes()["upload.key"]] {
			t.Fatalf("object %s delivered at %q but listed at %q", job.GetAttributes()["upload.key"], job.GetAttributes()["upload.etag"], got[job.GetAttributes()["upload.key"]])
		}
	}
	ordinal := l.job.GetAttributes()["upload.ordinal"]
	if l.Ordinal <= 0 || ordinal == "" {
		t.Fatalf("listing ordinal = %d / attribute %q", l.Ordinal, ordinal)
	}
	for _, job := range objects {
		if job.GetAttributes()["upload.ordinal"] != ordinal {
			t.Fatalf("object job ordinal %q, listing ordinal %q: one sync must carry one ordinal", job.GetAttributes()["upload.ordinal"], ordinal)
		}
	}
	attrs := l.job.GetAttributes()
	if attrs["datasource.source_id"] != source.ID || attrs["datasource.org_id"] != testOrg || attrs["datasource.boundary_id"] != source.BoundaryNodeID || l.job.GetQueue() != business.DatasourceIngestQueue {
		t.Fatalf("listing routing = %s %v", l.job.GetQueue(), attrs)
	}
}

// TestRunDatasourceSync_UploadNextSyncRedeliversUnderANewOrdinal pins that a
// later sync is a new order point whose jobs do not dedupe against the earlier
// one, so an object removed and put back unchanged is delivered again.
func TestRunDatasourceSync_UploadNextSyncRedeliversUnderANewOrdinal(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	useUploadBucket(svc, mirrorBucket())
	source := newUploadSource(t, svc)
	for i := 0; i < 2; i++ {
		if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
			t.Fatal(err)
		}
	}
	listings := uploadListings(t, producer)
	if len(listings) != 2 || listings[1].Ordinal <= listings[0].Ordinal {
		t.Fatalf("listings = %d, ordinals must increase per sync", len(listings))
	}
	if listings[0].job.GetIdempotencyKey() == listings[1].job.GetIdempotencyKey() {
		t.Fatal("two syncs' listings share an idempotency key")
	}
	objects := uploadObjectJobs(producer)
	if len(objects) != 4 || objects[0].GetIdempotencyKey() == objects[2].GetIdempotencyKey() {
		t.Fatal("the same unchanged object in two syncs shares an idempotency key; a put-back object would never be re-delivered")
	}
}

// TestRunDatasourceSync_UploadEmptiedFolderListsNothingExplicitly pins that an
// emptied folder sends an explicit empty inventory, which the store honours,
// rather than an absent one, which it refuses.
func TestRunDatasourceSync_UploadEmptiedFolderListsNothingExplicitly(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	useUploadBucket(svc, &fakeUploadBucket{})
	source := newUploadSource(t, svc)
	if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	listings := uploadListings(t, producer)
	if len(listings) != 1 || listings[0].Files == nil || len(*listings[0].Files) != 0 {
		t.Fatalf("emptied folder listing = %+v, want one listing with files: []", listings)
	}
}

// TestRunDatasourceSync_UploadFailedOrTruncatedListingNeverMirrors pins fail
// closed: a listing error sends nothing; a listing past the source's object cap
// delivers nothing, sends no listing and fails the sync terminally; and an
// object changed between the listing and its read fails the sync without a
// listing, for the next one to list afresh.
func TestRunDatasourceSync_UploadFailedOrTruncatedListingNeverMirrors(t *testing.T) {
	t.Run("listing error", func(t *testing.T) {
		producer := &recordingProducer{}
		svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
		fake := mirrorBucket()
		fake.listErr = errors.New("AccessDenied")
		useUploadBucket(svc, fake)
		source := newUploadSource(t, svc)
		// Connecting enqueues the source's first sync request; this subtest counts
		// everything on the producer, so that one is taken out of the way first.
		takeConnectSync(t, producer, source)
		if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err == nil {
			t.Fatal("a failed listing reported success")
		}
		if len(producer.jobs) != 0 {
			t.Fatalf("a failed listing enqueued %d job(s)", len(producer.jobs))
		}
	})
	t.Run("listing past the object cap", func(t *testing.T) {
		producer := &recordingProducer{}
		svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
		fake := mirrorBucket()
		useUploadBucket(svc, fake)
		cfg := uploadConfig()
		cfg.MaxObjects = 1
		source, err := svc.AddExistingSource(context.Background(), "actor-1", business.AddSourceInput{
			OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides", Credential: "secretkey", Upload: cfg,
		})
		if err != nil {
			t.Fatal(err)
		}
		takeConnectSync(t, producer, source)
		_, err = svc.RunDatasourceSync(context.Background(), source.ID)
		if !errors.Is(err, business.ErrUploadListingIncomplete) {
			t.Fatalf("listing past the cap: err = %v, want ErrUploadListingIncomplete", err)
		}
		if len(producer.jobs) != 0 || len(fake.read) != 0 {
			t.Fatalf("a listing past the cap enqueued %d job(s) and read %v; it must deliver nothing", len(producer.jobs), fake.read)
		}
		if _, err := svc.SyncDatasourceSource(context.Background(), "actor-1", testOrg, source.ID); err != nil {
			t.Fatal(err)
		}
		reqJob := producer.jobs[len(producer.jobs)-1]
		jobErr := svc.NewDatasourceSyncJobHandler()(context.Background(), &jobsv1.JobEnvelope{Queue: reqJob.GetQueue(), Topic: reqJob.GetTopic(), Attributes: reqJob.GetAttributes()})
		var procErr *jobs.ProcessingError
		if !errors.As(jobErr, &procErr) || procErr.Retryable {
			t.Fatalf("handler error = %v, want a terminal ProcessingError", jobErr)
		}
		if listings := uploadListings(t, producer); len(listings) != 0 {
			t.Fatal("the job handler's sync sent a listing of a folder past its cap")
		}
	})
	// The store's bucket gone between the listing and the reads: the service
	// answers every object of the batch NOT_FOUND, which ServiceBucket reports
	// as ErrObjectChanged. Tombstone authority is the closing listing alone, and
	// it is sent only after a successful listing in the same sync and the
	// delivery of every object in it — so this sync fails and deletes nothing.
	t.Run("every object gone after its listing", func(t *testing.T) {
		producer := &recordingProducer{}
		svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
		fake := mirrorBucket()
		fake.readErr = map[string]error{"loans/a.xlsx": objectstore.ErrObjectChanged, "loans/b.xlsx": objectstore.ErrObjectChanged}
		useUploadBucket(svc, fake)
		source := newUploadSource(t, svc)
		if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err == nil {
			t.Fatal("a sync whose every object vanished under it reported success")
		}
		if listings := uploadListings(t, producer); len(listings) != 0 {
			t.Fatalf("a sync that read none of its listed objects sent a listing %+v; it would tombstone the collection", listings)
		}
	})
	t.Run("object changed since its listing", func(t *testing.T) {
		producer := &recordingProducer{}
		svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
		fake := mirrorBucket()
		fake.readErr = map[string]error{"loans/b.xlsx": objectstore.ErrObjectChanged}
		useUploadBucket(svc, fake)
		source := newUploadSource(t, svc)
		if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err == nil {
			t.Fatal("a sync whose object changed under it reported success")
		}
		if listings := uploadListings(t, producer); len(listings) != 0 {
			t.Fatalf("a sync that could not read an object at its listed version sent a listing %+v", listings)
		}
	})
}
