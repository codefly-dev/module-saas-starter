package business_test

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/objectstore"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// refusedField is the field an INVALID_ARGUMENT refusal names, read from its
// BadRequest violation, after checking the message leads with the same path.
func refusedField(t *testing.T, err error) string {
	t.Helper()
	st := status.Convert(err)
	if err == nil || st.Code() != codes.InvalidArgument {
		t.Fatalf("err = %v, want INVALID_ARGUMENT", err)
	}
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok && len(br.GetFieldViolations()) == 1 {
			field := br.GetFieldViolations()[0].GetField()
			if !strings.HasPrefix(st.Message(), field+": ") {
				t.Fatalf("message %q does not lead with the field %q", st.Message(), field)
			}
			return field
		}
	}
	t.Fatalf("refusal %v carries no BadRequest violation", err)
	return ""
}

// An invalid key filter is refused when the source is added — by name, and
// before anything is stored — never discovered at sync.
func TestAddSource_UploadRefusesAnInvalidKeyFilterByField(t *testing.T) {
	cases := map[string]struct {
		mutate func(*business.UploadDatasourceConfig)
		field  string
	}{
		"invalid include":   {func(c *business.UploadDatasourceConfig) { c.IncludePatterns = []string{`ok`, `[z-a]`} }, "upload.include_patterns[1]"},
		"invalid exclude":   {func(c *business.UploadDatasourceConfig) { c.ExcludePatterns = []string{`^(?!drafts/)`} }, "upload.exclude_patterns[0]"},
		"invalid file type": {func(c *business.UploadDatasourceConfig) { c.FileExtensions = []string{"*.md"} }, "upload.file_extensions"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := newDatasourceFakeStore()
			svc, _ := newDatasourceService(store, &recordingProducer{}, nil)
			cfg := uploadConfig()
			tc.mutate(cfg)
			_, err := svc.AddExistingSource(context.Background(), "actor-1", business.AddSourceInput{
				OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides",
				Credential: "secretkey", Upload: cfg,
			})
			if got := refusedField(t, err); got != tc.field {
				t.Fatalf("refused field = %q, want %q", got, tc.field)
			}
			if sources, _ := svc.ListDatasourceSources(context.Background(), testOrg); len(sources) != 0 {
				t.Fatalf("a refused source was stored: %+v", sources)
			}
		})
	}
}

// filteredUploadSource connects an object-storage source with a key filter.
func filteredUploadSource(t *testing.T, svc *business.Service, include, exclude []string) *business.DatasourceSource {
	t.Helper()
	cfg := uploadConfig()
	cfg.IncludePatterns, cfg.ExcludePatterns = include, exclude
	source, err := svc.AddExistingSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides",
		Credential: "secretkey", Upload: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func uploadScopeOf(source *business.DatasourceSource) *business.DatasourceFetchScope {
	return &business.DatasourceFetchScope{
		IncludePatterns: source.Upload.IncludePatterns,
		ExcludePatterns: source.Upload.ExcludePatterns,
		FileExtensions:  source.Upload.FileExtensions,
	}
}

func TestUpdateSource_UploadRefusesAnInvalidKeyFilterByField(t *testing.T) {
	store := newDatasourceFakeStore()
	svc, audit := newDatasourceService(store, &recordingProducer{}, nil)
	source := filteredUploadSource(t, svc, []string{`^guides/`}, nil)

	_, _, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, IncludePatterns: []string{`^guides/`}, ExcludePatterns: []string{`a{1001}`},
		Expected: uploadScopeOf(source),
	})
	if got := refusedField(t, err); got != "exclude_patterns[0]" {
		t.Fatalf("refused field = %q, want exclude_patterns[0]", got)
	}
	_, _, err = svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, Paths: []string{"docs/"}, Expected: uploadScopeOf(source),
	})
	if got := refusedField(t, err); got != "paths" {
		t.Fatalf("refused field = %q, want paths: a bucket source has no paths", got)
	}
	stored, err := svc.GetDatasourceSource(context.Background(), testOrg, source.ID)
	if err != nil || !reflect.DeepEqual(stored.Upload.ExcludePatterns, []string(nil)) {
		t.Fatalf("a refused edit was written: %+v, %v", stored.Upload, err)
	}
	if entries := audit.entriesOf(business.EventDatasourceSourceUpdated); len(entries) != 0 {
		t.Fatalf("updated events = %d, want 0 for a refused edit", len(entries))
	}
}

func listedKeys(t *testing.T, l mirrorListing) []string {
	t.Helper()
	var keys []string
	for _, f := range *l.Files {
		keys = append(keys, f.Key)
	}
	sort.Strings(keys)
	return keys
}

func objectKeys(p *recordingProducer) []string {
	var keys []string
	for _, job := range uploadObjectJobs(p) {
		keys = append(keys, job.GetAttributes()["upload.key"])
	}
	sort.Strings(keys)
	return keys
}

// The mixed listing on the mirror seam: only matching keys are enqueued, an
// excluded key is never fetched, and the closing listing names only matches.
// Narrowing the filter through UpdateSource then re-syncs, and that sync's
// closing listing drops exactly the keys the narrower filter excludes — which
// is what tombstones them in the store — and nothing else.
func TestRunDatasourceSync_UploadKeyFilterNarrowingTombstonesOnlyTheExcluded(t *testing.T) {
	producer := &recordingProducer{}
	svc, audit := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	fake := &fakeUploadClient{
		entries: []objectstore.Entry{
			{Key: "kb/guides/a.md", ETag: "a"}, {Key: "kb/guides/b.pdf", ETag: "b"},
			{Key: "kb/drafts/c.md", ETag: "c"}, {Key: "kb/notes/d.md", ETag: "d"},
		},
		objects: map[string]objectstore.Object{},
	}
	for _, e := range fake.entries {
		fake.objects[e.Key] = objectstore.Object{Key: e.Key, Body: []byte(e.Key), ETag: e.ETag}
	}
	svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
	source := filteredUploadSource(t, svc, []string{`\.md$`, `^guides/`}, []string{`^drafts/`})
	takeConnectSync(t, producer, source)

	if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	wide := []string{"kb/guides/a.md", "kb/guides/b.pdf", "kb/notes/d.md"}
	if got := objectKeys(producer); !reflect.DeepEqual(got, wide) {
		t.Fatalf("enqueued = %v, want %v", got, wide)
	}
	sort.Strings(fake.fetched)
	if !reflect.DeepEqual(fake.fetched, wide) {
		t.Fatalf("fetched = %v, want only the matches %v: an excluded key is never read", fake.fetched, wide)
	}
	if listings := uploadListings(t, producer); len(listings) != 1 || !reflect.DeepEqual(listedKeys(t, listings[0]), wide) {
		t.Fatalf("closing listing does not name exactly the matches %v", wide)
	}

	// Narrow: exclude the notes folder as well.
	updated, jobID, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID,
		IncludePatterns: []string{`\.md$`, `^guides/`}, ExcludePatterns: []string{`^drafts/`, `^notes/`},
		Expected: uploadScopeOf(source),
	})
	if err != nil || jobID == "" {
		t.Fatalf("narrowing = %v, job %q; want a re-sync", err, jobID)
	}
	if entries := audit.entriesOf(business.EventDatasourceSourceUpdated); len(entries) != 1 {
		t.Fatalf("updated events = %d, want 1", len(entries))
	}
	if _, _, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, IncludePatterns: []string{`.*`}, Expected: uploadScopeOf(source),
	}); err != business.ErrDatasourceSourceScopeChanged {
		t.Fatalf("an edit against the scope before the narrowing = %v, want ErrDatasourceSourceScopeChanged", err)
	}

	producer.mu.Lock()
	producer.jobs = nil
	producer.mu.Unlock()
	if _, err := svc.RunDatasourceSync(context.Background(), updated.ID); err != nil {
		t.Fatal(err)
	}
	narrow := []string{"kb/guides/a.md", "kb/guides/b.pdf"}
	listings := uploadListings(t, producer)
	if len(listings) != 1 || !reflect.DeepEqual(listedKeys(t, listings[0]), narrow) {
		t.Fatalf("closing listing after narrowing does not name exactly %v: kb/notes/d.md must be the only key dropped", narrow)
	}
}
