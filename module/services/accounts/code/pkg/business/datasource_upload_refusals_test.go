package business_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	storagev0 "github.com/codefly-dev/service-object-storage/gen/codefly/storage/v0"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"accounts/pkg/business"
	"accounts/pkg/datasource/objectstore"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
)

// listingService is the object-storage service as the host's real client
// reaches it: it serves a binding and a batched read, and answers List however
// the test scripts — including the way a regressed service would.
type listingService struct {
	storagev0.ObjectStorageClient
	list func(*storagev0.ListRequest) (*storagev0.ListResult, error)
}

func (listingService) Capabilities(context.Context, *storagev0.CapabilitiesRequest, ...grpc.CallOption) (*storagev0.BackendCapabilities, error) {
	return &storagev0.BackendCapabilities{MaxObjectBytes: 5 << 20, MaxListLimit: 1000, MaxGetManyItems: 1000}, nil
}

func (l listingService) List(_ context.Context, r *storagev0.ListRequest, _ ...grpc.CallOption) (*storagev0.ListResult, error) {
	return l.list(r)
}

func runSyncJob(t *testing.T, svc *business.Service, producer *recordingProducer, source *business.DatasourceSource, jobSource string) error {
	t.Helper()
	if _, err := svc.SyncDatasourceSource(context.Background(), "actor-1", testOrg, source.ID); err != nil {
		t.Fatal(err)
	}
	req := producer.jobs[len(producer.jobs)-1]
	return svc.NewDatasourceSyncJobHandler()(context.Background(), &jobsv1.JobEnvelope{Queue: req.GetQueue(), Topic: req.GetTopic(), Source: jobSource, Attributes: req.GetAttributes()})
}

func processingFailure(t *testing.T, err error) *jobs.ProcessingError {
	t.Helper()
	var failure *jobs.ProcessingError
	if !errors.As(err, &failure) {
		t.Fatalf("job error = %v, want a classified ProcessingError", err)
	}
	return failure
}

// TestUploadSyncNeverEmptiesASourceThatHasFiles is the host-owned invariant
// the owner set on review r2's g2: a sync never applies a deletion delta that
// empties a source. A listing that comes back empty for a source whose last
// closing snapshot named objects is refused with a named reason, closes
// nothing, and leaves the collection as it is — whatever the service answers.
// Driven through the host's real client and the production job handler, with
// the two service regressions the review injected: a rewritten scope that
// lists nothing, and a truncation discarded into an empty last page. Both
// answer OK with no objects and no continuation token.
func TestUploadSyncNeverEmptiesASourceThatHasFiles(t *testing.T) {
	for _, regression := range []string{"rewritten scope", "discarded truncation"} {
		t.Run(regression, func(t *testing.T) {
			producer := &recordingProducer{}
			svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
			useUploadBucket(svc, &fakeUploadBucket{objects: []fakeObject{{key: "/reports/a.md", etag: "a", body: "A"}}})
			cfg := uploadConfig()
			cfg.Prefix = "/reports/"
			source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
				OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "reports", Credential: "secretkey", Upload: cfg,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
				t.Fatal(err)
			}
			if listings := uploadListings(t, producer); len(listings) != 1 || len(*listings[0].Files) != 1 {
				t.Fatalf("the first sync's closing listing = %+v, want one file", listings)
			}

			if err := svc.SetObjectStorage(context.Background(), listingService{list: func(*storagev0.ListRequest) (*storagev0.ListResult, error) {
				return &storagev0.ListResult{}, nil
			}}); err != nil {
				t.Fatal(err)
			}
			_, err = svc.RunDatasourceSync(context.Background(), source.ID)
			if !errors.Is(err, business.ErrUploadEmptyListingRefused) {
				t.Fatalf("an empty listing for a source with files = %v, want ErrUploadEmptyListingRefused", err)
			}
			if listings := uploadListings(t, producer); len(listings) != 1 {
				t.Fatalf("an empty listing for a source with files closed with %d listings; it must close none", len(listings)-1)
			}
			failure := processingFailure(t, runSyncJob(t, svc, producer, source, "github.sync.request"))
			if failure.Retryable || failure.Failure.GetCode() != "datasource.upload_empty_listing_refused" {
				t.Fatalf("job disposition = %+v, want the terminal empty-listing refusal", failure)
			}
		})
	}
}

// TestUploadSyncClosesAnEmptyListingOnlyWhereNothingIsMirrored: the invariant
// refuses only what would empty a source with files. A source that never
// synced, or whose last snapshot named nothing, closes an empty listing as
// before; a source synced before the count was recorded is treated as having
// files.
func TestUploadSyncClosesAnEmptyListingOnlyWhereNothingIsMirrored(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, nil)
	useUploadBucket(svc, &fakeUploadBucket{})
	add := func(label string) *business.DatasourceSource {
		source, err := svc.AddExistingSource(context.Background(), "actor-1", business.AddSourceInput{
			OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: label, Credential: "secretkey", Upload: uploadConfig(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	fresh := add("fresh")
	if _, err := svc.RunDatasourceSync(context.Background(), fresh.ID); err != nil {
		t.Fatalf("an empty listing for a never-synced source = %v, want it closed", err)
	}
	if _, err := svc.RunDatasourceSync(context.Background(), fresh.ID); err != nil {
		t.Fatalf("an empty listing for a source whose last snapshot was empty = %v, want it closed", err)
	}
	if listings := uploadListings(t, producer); len(listings) != 2 {
		t.Fatalf("closing listings = %d, want both empty syncs closed", len(listings))
	}

	legacy := add("legacy")
	synced := time.Now().Add(-time.Hour)
	store.sources[legacy.ID].LastSyncedAt = &synced
	if _, err := svc.RunDatasourceSync(context.Background(), legacy.ID); !errors.Is(err, business.ErrUploadEmptyListingRefused) {
		t.Fatalf("an empty listing for a source synced before the count existed = %v, want it refused", err)
	}
}

// TestUploadRefusalsGetTheJobPolicyTheirReasonNames is review r2's g3: each
// reason the object-storage service names gets its own action at the job, not
// one generic retry. Driven through the host's real client and the production
// job handler.
func TestUploadRefusalsGetTheJobPolicyTheirReasonNames(t *testing.T) {
	refusal := func(code codes.Code, reason string, details ...*errdetails.RetryInfo) error {
		st, _ := status.New(code, "refused").WithDetails(&errdetails.ErrorInfo{Domain: "object-storage.codefly.dev", Reason: reason})
		for _, d := range details {
			st, _ = st.WithDetails(d)
		}
		return st.Err()
	}
	for _, tc := range []struct {
		reason    string
		err       error
		code      string
		retryable bool
		degraded  string // the reason the source is parked with, or "" when it is not
	}{
		{"DESTINATION_REFUSED", refusal(codes.PermissionDenied, "DESTINATION_REFUSED"), "datasource.upload_destination_refused", false, business.ObjectStorageEndpointRefusedDegradeReason().String()},
		{"CREDENTIAL_REJECTED", refusal(codes.PermissionDenied, "CREDENTIAL_REJECTED"), "datasource.credential_rejected", false, business.ObjectStorageCredentialRejectedDegradeReason().String()},
		{"LISTING_INCOMPLETE", refusal(codes.Internal, "LISTING_INCOMPLETE"), "datasource.upload_listing_incomplete", false, ""},
		{"LISTING_TOO_LONG", refusal(codes.ResourceExhausted, "LISTING_TOO_LONG"), "datasource.upload_listing_incomplete", false, ""},
		{"RESPONSE_TOO_LARGE", refusal(codes.ResourceExhausted, "RESPONSE_TOO_LARGE"), "datasource.upload_listing_incomplete", false, ""},
		{"STORE_THROTTLED", refusal(codes.ResourceExhausted, "STORE_THROTTLED", &errdetails.RetryInfo{RetryDelay: durationpb.New(time.Minute)}), "datasource.rate_limited", true, ""},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			producer := &recordingProducer{}
			store := newDatasourceFakeStore()
			svc, _ := newDatasourceService(store, producer, nil)
			if err := svc.SetObjectStorage(context.Background(), listingService{list: func(*storagev0.ListRequest) (*storagev0.ListResult, error) { return nil, tc.err }}); err != nil {
				t.Fatal(err)
			}
			source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
				OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides", Credential: "secretkey", Upload: uploadConfig(),
			})
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			failure := processingFailure(t, runSyncJob(t, svc, producer, source, "github.sync.request"))
			if failure.Failure.GetCode() != tc.code || failure.Retryable != tc.retryable {
				t.Fatalf("disposition = %s retryable=%v, want %s retryable=%v", failure.Failure.GetCode(), failure.Retryable, tc.code, tc.retryable)
			}
			if tc.retryable && !failure.NotBefore.After(start.Add(30*time.Second)) {
				t.Fatalf("a throttle retries at %v, want no earlier than the store's reset", failure.NotBefore)
			}
			got := store.sources[source.ID]
			if tc.degraded != "" && (got.Status != business.DatasourceStatusDegraded || got.StatusReason != tc.degraded) {
				t.Fatalf("source = %s %q, want degraded with %q", got.Status, got.StatusReason, tc.degraded)
			}
			if tc.degraded == "" && got.Status == business.DatasourceStatusDegraded {
				t.Fatalf("a %s refusal parked the source", tc.reason)
			}
			if listings := uploadListings(t, producer); len(listings) != 0 {
				t.Fatalf("a refused sync closed with %d listings", len(listings))
			}
		})
	}
}

// degradeFailingStore is a store whose park write fails while broken is set.
type degradeFailingStore struct {
	*datasourceFakeStore
	broken bool
}

func (s *degradeFailingStore) MarkDatasourceSourceDegraded(ctx context.Context, id string, reason business.DatasourceDegradeReason) error {
	if s.broken {
		return errors.New("store unavailable")
	}
	return s.datasourceFakeStore.MarkDatasourceSourceDegraded(ctx, id, reason)
}

// TestUploadRefusalWhoseParkFailsRetries: the park is what makes a refused
// source's stop visible, so a refusal whose park write fails is not terminal —
// a terminal job would leave the source reading active with nothing on it to
// say why it stopped. The job retries instead, and the next attempt parks it.
// The rule the GitHub public-source park already followed; both now share it.
func TestUploadRefusalWhoseParkFailsRetries(t *testing.T) {
	refused, _ := status.New(codes.PermissionDenied, "refused").WithDetails(&errdetails.ErrorInfo{Domain: "object-storage.codefly.dev", Reason: "DESTINATION_REFUSED"})
	producer := &recordingProducer{}
	store := &degradeFailingStore{datasourceFakeStore: newDatasourceFakeStore(), broken: true}
	svc, _ := newDatasourceService(store, producer, nil)
	if err := svc.SetObjectStorage(context.Background(), listingService{list: func(*storagev0.ListRequest) (*storagev0.ListResult, error) { return nil, refused.Err() }}); err != nil {
		t.Fatal(err)
	}
	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides", Credential: "secretkey", Upload: uploadConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = runSyncJob(t, svc, producer, source, "github.sync.request")
	var failure *jobs.ProcessingError
	if err == nil || (errors.As(err, &failure) && !failure.Retryable) {
		t.Fatalf("a refusal whose park failed = %v, want a retryable failure", err)
	}
	if errors.Is(err, objectstore.ErrDestinationRefused) {
		t.Fatalf("a refusal whose park failed still carries the refusal: %v", err)
	}
	if got := store.sources[source.ID]; got.Status == business.DatasourceStatusDegraded {
		t.Fatalf("source = %s, want it as it was while the park cannot be written", got.Status)
	}

	store.broken = false
	if failure := processingFailure(t, runSyncJob(t, svc, producer, source, "github.sync.request")); failure.Retryable || failure.Failure.GetCode() != "datasource.upload_destination_refused" {
		t.Fatalf("retry = %s retryable=%v, want the terminal refusal once parked", failure.Failure.GetCode(), failure.Retryable)
	}
	if got := store.sources[source.ID]; got.Status != business.DatasourceStatusDegraded || got.StatusReason != business.ObjectStorageEndpointRefusedDegradeReason().String() {
		t.Fatalf("source = %s %q, want parked by the retry", got.Status, got.StatusReason)
	}
}

// TestUploadSyncSkipsAnObjectTheServiceCapsAndContinues: an object the
// object-storage service refuses as over its cap is skipped and the sync goes
// on — the rest of its batch and every later batch are delivered — then
// surfaces it without a closing listing that would tombstone it. Seventy-five
// objects listed at 900 KiB make two batches (a batch is bounded at 64 MiB);
// the capped one is in the first.
func TestUploadSyncSkipsAnObjectTheServiceCapsAndContinues(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	var objects []fakeObject
	for i := 0; i < 75; i++ {
		objects = append(objects, fakeObject{key: fmt.Sprintf("kb/%04d.md", i), etag: fmt.Sprint(i), body: "x", size: 900 << 10})
	}
	useUploadBucket(svc, &fakeUploadBucket{
		objects:   objects,
		readErr:   map[string]error{"kb/0003.md": &objectstore.TooLargeError{Keys: []string{"kb/0003.md"}}},
		skipOnErr: true,
	})
	cfg := uploadConfig()
	source, err := svc.AddExistingSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides", Credential: "secretkey", Upload: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	takeConnectSync(t, producer, source)
	enqueued, err := svc.RunDatasourceSync(context.Background(), source.ID)
	if err == nil {
		t.Fatal("a sync with a skipped object reported a clean success")
	}
	if enqueued != 74 {
		t.Fatalf("delivered %d objects, want every object but the capped one, across both batches", enqueued)
	}
	if listings := uploadListings(t, producer); len(listings) != 0 {
		t.Fatalf("a sync that skipped an object closed with a listing; it would tombstone it")
	}
}
