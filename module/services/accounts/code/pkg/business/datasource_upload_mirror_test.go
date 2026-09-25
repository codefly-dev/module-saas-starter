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

func mirrorClient() *fakeUploadClient {
	return &fakeUploadClient{
		entries: []objectstore.Entry{{Key: "loans/a.xlsx", ETag: "list-a"}, {Key: "loans/", ETag: "dir"}, {Key: "loans/b.xlsx", ETag: "list-b"}},
		objects: map[string]objectstore.Object{
			"loans/a.xlsx": {Key: "loans/a.xlsx", Body: []byte("A"), ETag: "get-a"},
			"loans/b.xlsx": {Key: "loans/b.xlsx", Body: []byte("B"), ETag: "get-b"},
		},
	}
}

// TestRunDatasourceSync_UploadClosesWithTheCompleteListing pins the mirror: a
// sync that saw the whole folder sends, after every object job, one listing of
// exactly the objects it delivered, versioned the way their jobs were, under
// the one ordinal all of the sync's jobs carry.
func TestRunDatasourceSync_UploadClosesWithTheCompleteListing(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	fake := mirrorClient()
	svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
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
	fake := mirrorClient()
	svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
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
	fake := &fakeUploadClient{}
	svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
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
// closed: a listing error sends nothing, and a truncated listing still
// delivers what it listed but sends no listing and fails the sync terminally.
func TestRunDatasourceSync_UploadFailedOrTruncatedListingNeverMirrors(t *testing.T) {
	t.Run("listing error", func(t *testing.T) {
		producer := &recordingProducer{}
		svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
		fake := mirrorClient()
		fake.listErr = errors.New("AccessDenied")
		svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
		source := newUploadSource(t, svc)
		if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err == nil {
			t.Fatal("a failed listing reported success")
		}
		if len(producer.jobs) != 0 {
			t.Fatalf("a failed listing enqueued %d job(s)", len(producer.jobs))
		}
	})
	t.Run("truncated listing", func(t *testing.T) {
		producer := &recordingProducer{}
		store := newDatasourceFakeStore()
		svc, _ := newDatasourceService(store, producer, nil)
		fake := mirrorClient()
		fake.incomplete = true
		svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
		source := newUploadSource(t, svc)
		_, err := svc.RunDatasourceSync(context.Background(), source.ID)
		if !errors.Is(err, business.ErrUploadListingIncomplete) {
			t.Fatalf("truncated listing: err = %v, want ErrUploadListingIncomplete", err)
		}
		if len(uploadObjectJobs(producer)) != 2 {
			t.Fatal("a truncated listing stopped delivering the objects it did list")
		}
		if listings := uploadListings(t, producer); len(listings) != 0 {
			t.Fatalf("a truncated listing was sent as complete: %+v", listings)
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
			t.Fatal("the job handler's sync sent a listing of a truncated folder")
		}
	})
	t.Run("vanished object is left out of the listing", func(t *testing.T) {
		producer := &recordingProducer{}
		svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
		fake := mirrorClient()
		fake.fetchErr = map[string]error{"loans/b.xlsx": objectstore.ErrObjectNotFound}
		svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
		source := newUploadSource(t, svc)
		if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
			t.Fatal(err)
		}
		listings := uploadListings(t, producer)
		if len(listings) != 1 || len(*listings[0].Files) != 1 || (*listings[0].Files)[0].Key != "loans/a.xlsx" {
			t.Fatalf("listing after a vanished object = %+v", listings)
		}
	})
}
