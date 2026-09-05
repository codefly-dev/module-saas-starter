package business_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource/apisource"
	"accounts/pkg/datasource/crawler"
	"accounts/pkg/datasource/github"
	"accounts/pkg/datasource/objectstore"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

// datasourceFakeStore is a partial fake: it embeds Store (panics on any
// unimplemented method) and keeps sources in memory keyed by id.
type datasourceFakeStore struct {
	business.Store
	mu      sync.Mutex
	sources map[string]*business.DatasourceSource
}

func newDatasourceFakeStore() *datasourceFakeStore {
	return &datasourceFakeStore{sources: map[string]*business.DatasourceSource{}}
}

func (f *datasourceFakeStore) WithOrgTx(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func (f *datasourceFakeStore) InsertDatasourceSource(_ context.Context, source *business.DatasourceSource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *source
	f.sources[source.ID] = &cp
	return nil
}

func (f *datasourceFakeStore) ListDatasourceSources(_ context.Context, orgID string) ([]*business.DatasourceSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*business.DatasourceSource
	for _, s := range f.sources {
		if s.OrgID == orgID {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *datasourceFakeStore) GetDatasourceSource(_ context.Context, orgID, id string) (*business.DatasourceSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sources[id]; ok && s.OrgID == orgID {
		cp := *s
		return &cp, nil
	}
	return nil, nil
}

func (f *datasourceFakeStore) GetDatasourceSourceByID(_ context.Context, id string) (*business.DatasourceSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sources[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, nil
}

func (f *datasourceFakeStore) DeleteDatasourceSource(_ context.Context, orgID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sources[id]; ok && s.OrgID == orgID {
		delete(f.sources, id)
	}
	return nil
}

func (f *datasourceFakeStore) SetDatasourceSourceSynced(_ context.Context, orgID, id string, syncedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sources[id]; ok && s.OrgID == orgID {
		at := syncedAt
		s.LastSyncedAt = &at
	}
	return nil
}

// purposeCipher is a deterministic, purpose-binding fake: the envelope embeds
// the purpose, so DecryptSecret fails closed if replayed under a different one —
// mirroring the Vault-transit binding the real cipher enforces.
type purposeCipher struct{}

func (purposeCipher) EncryptSecret(_ context.Context, purpose, plaintext string) (string, error) {
	return "enc:" + purpose + ":" + plaintext, nil
}

func (purposeCipher) DecryptSecret(_ context.Context, purpose, envelope string) (string, error) {
	rest, ok := strings.CutPrefix(envelope, "enc:"+purpose+":")
	if !ok {
		return "", errors.New("purpose mismatch")
	}
	return rest, nil
}

// recordingProducer captures every enqueue for assertions.
type recordingProducer struct {
	mu   sync.Mutex
	jobs []*jobsv1.NewJob
}

func (p *recordingProducer) EnqueueJob(_ context.Context, req *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs = append(p.jobs, req.GetJob())
	return &jobsv1.EnqueueJobResponse{
		JobId:       "job",
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

// fakeGitHub returns fixed repository contents without a live api.github.com.
// A path mapped to a nil (absent) content entry surfaces the error errs[path].
type fakeGitHub struct {
	defaultBranch string
	commit        string
	files         []github.File
	content       map[string][]byte
	errs          map[string]error
}

func (f *fakeGitHub) DefaultBranch(context.Context, string) (string, error) {
	return f.defaultBranch, nil
}
func (f *fakeGitHub) ResolveCommit(context.Context, string, string) (string, error) {
	return f.commit, nil
}
func (f *fakeGitHub) ListFiles(context.Context, string, string, []string) ([]github.File, error) {
	return f.files, nil
}
func (f *fakeGitHub) GetFileContent(_ context.Context, _, _, path string) ([]byte, error) {
	if err, ok := f.errs[path]; ok {
		return nil, err
	}
	return f.content[path], nil
}

// recordingAudit captures emitted audit entries so a test can assert an RPC
// actually produces the audit event its policy declares.
type recordingAudit struct {
	mu      sync.Mutex
	entries []business.AuditEntry
}

func (a *recordingAudit) Emit(_ context.Context, entry business.AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, entry)
}

func (a *recordingAudit) types() []business.EventType {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]business.EventType, len(a.entries))
	for i, e := range a.entries {
		out[i] = e.EventType
	}
	return out
}

func newDatasourceService(store business.Store, producer *recordingProducer, gh business.GitHubContentClient) (*business.Service, *recordingAudit) {
	svc, _ := business.NewService(store)
	svc.SetDatasourceConnector(purposeCipher{}, producer, "")
	audit := &recordingAudit{}
	svc.SetAuditEmitter(audit)
	if gh != nil {
		svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient { return gh })
	}
	return svc, audit
}

const testOrg = "11111111-1111-1111-1111-111111111111"

func addSource(t *testing.T, svc *business.Service, in business.AddGitHubSourceInput) *business.DatasourceSource {
	t.Helper()
	source, err := svc.AddGitHubSource(context.Background(), "actor-1", in)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestAddGitHubSource_EncryptsPerSourceAndOmitsSecrets(t *testing.T) {
	svc, audit := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)

	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID:            testOrg,
		Repo:             "acme/docs",
		Paths:            []string{"docs", "docs"}, // duplicate is normalized away
		TargetCollection: "wiki",
		AccessToken:      "ghp_secret",
		WebhookSecret:    "whsec",
	})
	if source.Provider != business.DatasourceProviderGitHub || source.Status != business.DatasourceStatusActive {
		t.Fatalf("provider/status = %q/%q", source.Provider, source.Status)
	}
	if len(source.Paths) != 1 || source.Paths[0] != "docs" {
		t.Fatalf("paths = %v, want [docs]", source.Paths)
	}
	// The stored envelopes are bound to this source id's purposes.
	wantCred := "enc:" + business.DatasourceConnectorSecretPurpose(source.ID) + ":ghp_secret"
	wantHook := "enc:" + business.DatasourceWebhookSecretPurpose(source.ID) + ":whsec"
	if source.CredentialSecretRef != wantCred {
		t.Fatalf("credential ref = %q, want %q", source.CredentialSecretRef, wantCred)
	}
	if source.WebhookSecretRef != wantHook || !source.WebhookConfigured() {
		t.Fatalf("webhook ref = %q, configured=%v", source.WebhookSecretRef, source.WebhookConfigured())
	}
	// The RPC's declared audit event must actually be emitted.
	if got := audit.types(); len(got) != 1 || got[0] != business.EventDatasourceSourceAdded {
		t.Fatalf("audit events = %v, want [%s]", got, business.EventDatasourceSourceAdded)
	}
}

func TestAddGitHubSource_Validation(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	base := business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", TargetCollection: "wiki", AccessToken: "t"}
	cases := map[string]business.AddGitHubSourceInput{
		"missing repo":       {OrgID: testOrg, TargetCollection: "wiki", AccessToken: "t"},
		"bad repo":           mut(base, func(i *business.AddGitHubSourceInput) { i.Repo = "not-a-repo" }),
		"missing collection": mut(base, func(i *business.AddGitHubSourceInput) { i.TargetCollection = "" }),
		"missing token":      mut(base, func(i *business.AddGitHubSourceInput) { i.AccessToken = "" }),
	}
	for name, in := range cases {
		if _, err := svc.AddGitHubSource(context.Background(), "actor-1", in); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func TestSigningSecret_ResolvesPerSource(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)

	withHook := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", TargetCollection: "wiki", AccessToken: "t", WebhookSecret: "whsec",
	})
	secret, err := svc.SigningSecret(context.Background(), withHook.ID)
	if err != nil || secret != "whsec" {
		t.Fatalf("SigningSecret = %q, %v; want whsec", secret, err)
	}

	noHook := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/other", TargetCollection: "wiki", AccessToken: "t",
	})
	if _, err := svc.SigningSecret(context.Background(), noHook.ID); !errors.Is(err, business.ErrDatasourceSourceNotFound) {
		t.Fatalf("unconfigured source: err = %v, want ErrDatasourceSourceNotFound", err)
	}
	if _, err := svc.SigningSecret(context.Background(), "unknown"); !errors.Is(err, business.ErrDatasourceSourceNotFound) {
		t.Fatalf("unknown source: err = %v, want ErrDatasourceSourceNotFound", err)
	}
}

// TestSyncDatasourceSource_SchedulesRequest proves the RPC does NOT pull inline:
// it enqueues exactly one sync-request job on the internal request queue, emits
// the sync audit event, and returns the job id.
func TestSyncDatasourceSource_SchedulesRequest(t *testing.T) {
	producer := &recordingProducer{}
	svc, audit := newDatasourceService(newDatasourceFakeStore(), producer, &fakeGitHub{})
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", TargetCollection: "wiki", AccessToken: "t",
	})

	jobID, err := svc.SyncDatasourceSource(context.Background(), "actor-1", testOrg, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if jobID != "job" {
		t.Fatalf("job id = %q, want the enqueued job id", jobID)
	}
	if len(producer.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want exactly 1 sync request (no inline pull)", len(producer.jobs))
	}
	if producer.jobs[0].GetQueue() != business.DatasourceSyncRequestQueue {
		t.Fatalf("queue = %q, want %q", producer.jobs[0].GetQueue(), business.DatasourceSyncRequestQueue)
	}
	// The add above also audited; the sync must add its own event on top.
	got := audit.types()
	if len(got) == 0 || got[len(got)-1] != business.EventDatasourceSourceSynced {
		t.Fatalf("audit events = %v, want last = %s", got, business.EventDatasourceSourceSynced)
	}
}

func TestRunDatasourceSync_EnqueuesPerFile(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{
		defaultBranch: "trunk",
		commit:        "c0ffee",
		files:         []github.File{{Path: "docs/a.md", SHA: "sa"}, {Path: "docs/b.md", SHA: "sb"}},
		content:       map[string][]byte{"docs/a.md": []byte("A"), "docs/b.md": []byte("B")},
	}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", TargetCollection: "wiki", AccessToken: "t",
	})

	enqueued, err := svc.RunDatasourceSync(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 || len(producer.jobs) != 2 {
		t.Fatalf("enqueued=%d jobs=%d, want 2", enqueued, len(producer.jobs))
	}
	job := producer.jobs[0]
	if job.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_INBOX {
		t.Fatalf("direction = %v, want INBOX", job.GetDirection())
	}
	// Ingest deliveries are Global-scoped to match the webhook producer on the
	// shared queue; the org travels in an attribute.
	if !job.GetScope().GetGlobal() {
		t.Fatalf("scope = %v, want Global", job.GetScope())
	}
	attrs := job.GetAttributes()
	if attrs["datasource.source_id"] != source.ID || attrs["datasource.org_id"] != testOrg ||
		attrs["github.ref"] != "trunk" || attrs["github.commit"] != "c0ffee" ||
		attrs["github.change_type"] != "added" || attrs["datasource.target_collection"] != "wiki" {
		t.Fatalf("attributes = %v", attrs)
	}
	if producer.jobs[0].GetIdempotencyKey() == producer.jobs[1].GetIdempotencyKey() {
		t.Fatal("idempotency keys collided across files")
	}
}

// TestRunDatasourceSync_SkipsUnfetchableFiles proves a single oversized or
// missing file no longer aborts the whole walk (finding #2): the good files
// still get enqueued.
func TestRunDatasourceSync_SkipsUnfetchableFiles(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{
		commit: "c1",
		files: []github.File{
			{Path: "docs/ok.md", SHA: "s1"},
			{Path: "docs/big.png", SHA: "s2"},
			{Path: "docs/gone.md", SHA: "s3"},
		},
		content: map[string][]byte{"docs/ok.md": []byte("OK")},
		errs: map[string]error{
			"docs/big.png": github.ErrFileTooLarge,
			"docs/gone.md": github.ErrNotFound,
		},
	}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", TargetCollection: "wiki", AccessToken: "t",
	})

	enqueued, err := svc.RunDatasourceSync(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("sync must not abort on a skippable file: %v", err)
	}
	if enqueued != 1 || len(producer.jobs) != 1 {
		t.Fatalf("enqueued=%d jobs=%d, want only the fetchable file", enqueued, len(producer.jobs))
	}
	if producer.jobs[0].GetAttributes()["github.path"] != "docs/ok.md" {
		t.Fatalf("enqueued the wrong file: %v", producer.jobs[0].GetAttributes())
	}
}

// TestRunDatasourceSync_CommitKeyedIdempotency proves finding #6: a re-sync at
// the same commit yields the same idempotency key (documents dedupes), while a
// revert to earlier content under a NEW commit yields a distinct key so it is
// re-delivered rather than silently dropped.
func TestRunDatasourceSync_CommitKeyedIdempotency(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{
		commit:  "commitA",
		files:   []github.File{{Path: "docs/a.md", SHA: "blobA"}},
		content: map[string][]byte{"docs/a.md": []byte("A")},
	}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", TargetCollection: "wiki", AccessToken: "t",
	})

	if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	// Re-sync at the SAME commit → same key.
	if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	// Content reverts to A under a NEW commit → distinct key.
	gh.commit = "commitC"
	if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	if len(producer.jobs) != 3 {
		t.Fatalf("want 3 enqueue calls, got %d", len(producer.jobs))
	}
	keyA1, keyA2, keyC := producer.jobs[0].GetIdempotencyKey(), producer.jobs[1].GetIdempotencyKey(), producer.jobs[2].GetIdempotencyKey()
	if keyA1 != keyA2 {
		t.Fatal("same commit must produce the same idempotency key (dedup)")
	}
	if keyC == keyA1 {
		t.Fatal("a new commit must produce a distinct key so a revert is delivered, not dropped")
	}
}

func mut(in business.AddGitHubSourceInput, f func(*business.AddGitHubSourceInput)) business.AddGitHubSourceInput {
	f(&in)
	return in
}

// fakeAPIClient returns a fixed API fetch result without a live endpoint.
type fakeAPIClient struct {
	result *apisource.Result
	err    error
	calls  int
}

func (f *fakeAPIClient) Fetch(context.Context) (*apisource.Result, error) {
	f.calls++
	return f.result, f.err
}

func apiConfig() *business.APIDatasourceConfig {
	return &business.APIDatasourceConfig{
		BaseURL:        "https://api.example.com",
		ResourcePath:   "/v1/docs",
		CredentialKind: business.APICredentialKindBearer,
	}
}

func TestAddSource_APIStoresConfigAndEncryptsCredential(t *testing.T) {
	svc, audit := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)

	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID:            testOrg,
		Provider:         business.DatasourceProviderAPI,
		TargetCollection: "wiki",
		Credential:       "sekret",
		API:              apiConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.Provider != business.DatasourceProviderAPI || source.Status != business.DatasourceStatusActive {
		t.Fatalf("provider/status = %q/%q", source.Provider, source.Status)
	}
	if source.Repo != "" {
		t.Fatalf("api source must have no repo, got %q", source.Repo)
	}
	if source.API == nil || source.API.BaseURL != "https://api.example.com" ||
		source.API.CredentialKind != business.APICredentialKindBearer {
		t.Fatalf("api config = %+v", source.API)
	}
	wantCred := "enc:" + business.DatasourceConnectorSecretPurpose(source.ID) + ":sekret"
	if source.CredentialSecretRef != wantCred {
		t.Fatalf("credential ref = %q, want %q", source.CredentialSecretRef, wantCred)
	}
	if source.WebhookConfigured() {
		t.Fatal("api source must not be webhook-configured")
	}
	if got := audit.types(); len(got) != 1 || got[0] != business.EventDatasourceSourceAdded {
		t.Fatalf("audit events = %v", got)
	}
}

func TestAddSource_APIRejectsWebhookSecret(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	_, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderAPI, TargetCollection: "wiki",
		Credential: "sekret", API: apiConfig(), WebhookSecret: "whsec",
	})
	if err == nil {
		t.Fatal("api provider must reject a webhook secret it cannot verify")
	}
}

func TestAddSource_Validation(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	base := business.AddSourceInput{OrgID: testOrg, Provider: business.DatasourceProviderAPI, TargetCollection: "wiki", Credential: "c", API: apiConfig()}
	cases := map[string]business.AddSourceInput{
		"missing credential":  {OrgID: testOrg, Provider: business.DatasourceProviderAPI, TargetCollection: "wiki", API: apiConfig()},
		"missing collection":  {OrgID: testOrg, Provider: business.DatasourceProviderAPI, Credential: "c", API: apiConfig()},
		"unknown provider":    {OrgID: testOrg, Provider: "gitlab", TargetCollection: "wiki", Credential: "c"},
		"api without config":  {OrgID: testOrg, Provider: business.DatasourceProviderAPI, TargetCollection: "wiki", Credential: "c"},
		"github without repo": {OrgID: testOrg, Provider: business.DatasourceProviderGitHub, TargetCollection: "wiki", Credential: "c"},
	}
	cases["bad base url"] = withAPI(base, func(c *business.APIDatasourceConfig) { c.BaseURL = "ftp://x" })
	cases["bad credential kind"] = withAPI(base, func(c *business.APIDatasourceConfig) { c.CredentialKind = "oauth" })
	cases["header kind without header"] = withAPI(base, func(c *business.APIDatasourceConfig) {
		c.CredentialKind = business.APICredentialKindHeader
	})
	for name, in := range cases {
		if _, err := svc.AddSource(context.Background(), "actor-1", in); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func withAPI(in business.AddSourceInput, f func(*business.APIDatasourceConfig)) business.AddSourceInput {
	cfg := *in.API
	f(&cfg)
	in.API = &cfg
	return in
}

func TestAddSource_GitHubBranchThroughGenericCall(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderGitHub, TargetCollection: "wiki",
		Credential: "ghp", Repo: "acme/docs", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.Provider != business.DatasourceProviderGitHub || source.Repo != "acme/docs" || source.Branch != "main" {
		t.Fatalf("github source = %+v", source)
	}
}

// fakeCrawlerClient serves fixed pages without a live website, driven one URL at
// a time. fetchErr injects a per-URL error (e.g. crawler.ErrPageTooLarge or a
// generic failure); listErr fails the listing.
type fakeCrawlerClient struct {
	pages      []crawler.Page
	fetchErr   map[string]error
	listErr    error
	listCalls  int
	fetchCalls int
}

func (f *fakeCrawlerClient) List(context.Context) ([]string, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	urls := make([]string, len(f.pages))
	for i, p := range f.pages {
		urls[i] = p.URL
	}
	return urls, nil
}

func (f *fakeCrawlerClient) Fetch(_ context.Context, pageURL string) (crawler.Page, error) {
	f.fetchCalls++
	if err := f.fetchErr[pageURL]; err != nil {
		return crawler.Page{}, err
	}
	for _, p := range f.pages {
		if p.URL == pageURL {
			return p, nil
		}
	}
	return crawler.Page{}, errors.New("unknown page")
}

// fakeUploadClient serves fixed objects without a live object store, driven one
// key at a time. fetchErr injects a per-key error (e.g. objectstore.ErrObjectNotFound,
// objectstore.ErrObjectTooLarge, or a generic failure); listErr fails the listing.
type fakeUploadClient struct {
	entries    []objectstore.Entry
	objects    map[string]objectstore.Object
	fetchErr   map[string]error
	listErr    error
	listCalls  int
	fetchCalls int
}

func (f *fakeUploadClient) List(context.Context) ([]objectstore.Entry, error) {
	f.listCalls++
	return f.entries, f.listErr
}

func (f *fakeUploadClient) Fetch(_ context.Context, key string) (objectstore.Object, error) {
	f.fetchCalls++
	if err := f.fetchErr[key]; err != nil {
		return objectstore.Object{}, err
	}
	return f.objects[key], nil
}

func crawlerConfig() *business.CrawlerDatasourceConfig {
	return &business.CrawlerDatasourceConfig{SitemapURL: "https://docs.example.com/sitemap.xml"}
}

func uploadConfig() *business.UploadDatasourceConfig {
	return &business.UploadDatasourceConfig{
		Endpoint:    "https://s3.us-east-1.amazonaws.com",
		Region:      "us-east-1",
		Bucket:      "docs",
		Prefix:      "kb/",
		AccessKeyID: "AKIAEXAMPLE",
	}
}

func TestAddSource_CrawlerStoresConfigAndTakesNoCredential(t *testing.T) {
	svc, audit := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)

	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID:            testOrg,
		Provider:         business.DatasourceProviderCrawler,
		TargetCollection: "wiki",
		Crawler:          crawlerConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.Provider != business.DatasourceProviderCrawler || source.Crawler == nil ||
		source.Crawler.SitemapURL != "https://docs.example.com/sitemap.xml" {
		t.Fatalf("crawler source = %+v", source)
	}
	if source.CredentialSecretRef != "" {
		t.Fatalf("crawler must store no credential, got %q", source.CredentialSecretRef)
	}
	if source.WebhookConfigured() {
		t.Fatal("crawler must not be webhook-configured")
	}
	if got := audit.types(); len(got) != 1 || got[0] != business.EventDatasourceSourceAdded {
		t.Fatalf("audit events = %v", got)
	}
}

func TestAddSource_UploadStoresConfigAndEncryptsSecretKey(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)

	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID:            testOrg,
		Provider:         business.DatasourceProviderUpload,
		TargetCollection: "wiki",
		Credential:       "secretkey",
		Upload:           uploadConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.Upload == nil || source.Upload.Bucket != "docs" || source.Upload.AccessKeyID != "AKIAEXAMPLE" {
		t.Fatalf("upload config = %+v", source.Upload)
	}
	wantCred := "enc:" + business.DatasourceConnectorSecretPurpose(source.ID) + ":secretkey"
	if source.CredentialSecretRef != wantCred {
		t.Fatalf("credential ref = %q, want %q", source.CredentialSecretRef, wantCred)
	}
}

func TestAddSource_NewProviderValidation(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	cases := map[string]business.AddSourceInput{
		"crawler without config": {OrgID: testOrg, Provider: business.DatasourceProviderCrawler, TargetCollection: "wiki"},
		"crawler bad sitemap": {OrgID: testOrg, Provider: business.DatasourceProviderCrawler, TargetCollection: "wiki",
			Crawler: &business.CrawlerDatasourceConfig{SitemapURL: "ftp://x"}},
		"crawler with credential": {OrgID: testOrg, Provider: business.DatasourceProviderCrawler, TargetCollection: "wiki",
			Credential: "nope", Crawler: crawlerConfig()},
		"crawler with webhook": {OrgID: testOrg, Provider: business.DatasourceProviderCrawler, TargetCollection: "wiki",
			WebhookSecret: "whsec", Crawler: crawlerConfig()},
		"upload without config": {OrgID: testOrg, Provider: business.DatasourceProviderUpload, TargetCollection: "wiki", Credential: "c"},
		"upload without credential": {OrgID: testOrg, Provider: business.DatasourceProviderUpload, TargetCollection: "wiki",
			Upload: uploadConfig()},
		"upload bad endpoint": {OrgID: testOrg, Provider: business.DatasourceProviderUpload, TargetCollection: "wiki", Credential: "c",
			Upload: &business.UploadDatasourceConfig{Endpoint: "ftp://x", Region: "us-east-1", Bucket: "b", AccessKeyID: "k"}},
		"upload missing region": {OrgID: testOrg, Provider: business.DatasourceProviderUpload, TargetCollection: "wiki", Credential: "c",
			Upload: &business.UploadDatasourceConfig{Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "k"}},
		"upload with webhook": {OrgID: testOrg, Provider: business.DatasourceProviderUpload, TargetCollection: "wiki", Credential: "c",
			WebhookSecret: "whsec", Upload: uploadConfig()},
	}
	for name, in := range cases {
		if _, err := svc.AddSource(context.Background(), "actor-1", in); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func newCrawlerSource(t *testing.T, svc *business.Service) *business.DatasourceSource {
	t.Helper()
	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderCrawler, TargetCollection: "wiki", Crawler: crawlerConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func newUploadSource(t *testing.T, svc *business.Service) *business.DatasourceSource {
	t.Helper()
	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderUpload, TargetCollection: "wiki",
		Credential: "secretkey", Upload: uploadConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestRunDatasourceSync_CrawlerStreamsPerPage(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	fake := &fakeCrawlerClient{pages: []crawler.Page{
		{URL: "https://docs.example.com/a", Body: []byte("A"), ContentType: "text/html"},
		{URL: "https://docs.example.com/b", Body: []byte("B"), ContentType: "text/html"},
	}}
	svc.SetDatasourceCrawlerClientFactory(func(business.CrawlerDatasourceConfig) business.CrawlerContentClient { return fake })
	source := newCrawlerSource(t, svc)

	enqueued, err := svc.RunDatasourceSync(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	// One List then one Fetch per page — the whole site is never materialized.
	if enqueued != 2 || len(producer.jobs) != 2 || fake.listCalls != 1 || fake.fetchCalls != 2 {
		t.Fatalf("enqueued=%d jobs=%d list=%d fetch=%d, want 2/2/1/2", enqueued, len(producer.jobs), fake.listCalls, fake.fetchCalls)
	}
	job := producer.jobs[0]
	if job.GetTopic() != "datasource.crawler.sync" || !job.GetScope().GetGlobal() {
		t.Fatalf("topic=%q scope=%v", job.GetTopic(), job.GetScope())
	}
	if string(job.GetPayload()) != "A" || job.GetContentType() != "text/html" {
		t.Fatalf("payload=%q type=%q", job.GetPayload(), job.GetContentType())
	}
	attrs := job.GetAttributes()
	if attrs["datasource.source_id"] != source.ID || attrs["datasource.org_id"] != testOrg ||
		attrs["crawler.url"] != "https://docs.example.com/a" || attrs["crawler.content_sha"] == "" ||
		attrs["datasource.target_collection"] != "wiki" {
		t.Fatalf("attributes = %v", attrs)
	}
	if producer.jobs[0].GetIdempotencyKey() == producer.jobs[1].GetIdempotencyKey() {
		t.Fatal("idempotency keys collided across pages")
	}
}

// A page too large to deliver must surface as a sync error (the good pages still
// enqueue), never a silent drop reported as success.
func TestRunDatasourceSync_CrawlerSurfacesOversizedPage(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	fake := &fakeCrawlerClient{
		pages: []crawler.Page{
			{URL: "https://docs.example.com/small", Body: []byte("ok"), ContentType: "text/html"},
			{URL: "https://docs.example.com/huge"},
		},
		fetchErr: map[string]error{"https://docs.example.com/huge": crawler.ErrPageTooLarge},
	}
	svc.SetDatasourceCrawlerClientFactory(func(business.CrawlerDatasourceConfig) business.CrawlerContentClient { return fake })
	source := newCrawlerSource(t, svc)

	enqueued, err := svc.RunDatasourceSync(context.Background(), source.ID)
	if err == nil {
		t.Fatal("an oversized page must surface as an error, not a silent success")
	}
	if enqueued != 1 || len(producer.jobs) != 1 {
		t.Fatalf("the deliverable page must still enqueue: enqueued=%d jobs=%d", enqueued, len(producer.jobs))
	}
}

// A sitemap whose every page fails to fetch must fail the sync, not record an
// empty success that looks like a healthy but contentless site.
func TestRunDatasourceSync_CrawlerWholesaleFailureIsNotEmptySuccess(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	fake := &fakeCrawlerClient{
		pages: []crawler.Page{{URL: "https://docs.example.com/a"}, {URL: "https://docs.example.com/b"}},
		fetchErr: map[string]error{
			"https://docs.example.com/a": errors.New("timeout"),
			"https://docs.example.com/b": errors.New("timeout"),
		},
	}
	svc.SetDatasourceCrawlerClientFactory(func(business.CrawlerDatasourceConfig) business.CrawlerContentClient { return fake })
	source := newCrawlerSource(t, svc)

	if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err == nil {
		t.Fatal("a crawl that reached no page must fail, not report an empty success")
	}
	if len(producer.jobs) != 0 {
		t.Fatalf("no deliveries expected, got %d", len(producer.jobs))
	}
}

func TestRunDatasourceSync_UploadStreamsPerObject(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	var gotSecret string
	fake := &fakeUploadClient{
		entries: []objectstore.Entry{{Key: "kb/a.pdf", ETag: "etag-a"}, {Key: "kb/b.pdf", ETag: "etag-b"}},
		objects: map[string]objectstore.Object{
			"kb/a.pdf": {Key: "kb/a.pdf", Body: []byte("PDFA"), ContentType: "application/pdf", ETag: "etag-a"},
			"kb/b.pdf": {Key: "kb/b.pdf", Body: []byte("PDFB"), ContentType: "application/pdf", ETag: "etag-b"},
		},
	}
	svc.SetDatasourceUploadClientFactory(func(_ business.UploadDatasourceConfig, secret string) business.UploadContentClient {
		gotSecret = secret
		return fake
	})
	source := newUploadSource(t, svc)

	enqueued, err := svc.RunDatasourceSync(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 || len(producer.jobs) != 2 || fake.listCalls != 1 || fake.fetchCalls != 2 {
		t.Fatalf("enqueued=%d jobs=%d list=%d fetch=%d, want 2/2/1/2", enqueued, len(producer.jobs), fake.listCalls, fake.fetchCalls)
	}
	if gotSecret != "secretkey" {
		t.Fatalf("secret handed to connector = %q, want the decrypted secret key", gotSecret)
	}
	job := producer.jobs[0]
	if job.GetTopic() != "datasource.upload.sync" || !job.GetScope().GetGlobal() {
		t.Fatalf("topic=%q scope=%v", job.GetTopic(), job.GetScope())
	}
	if string(job.GetPayload()) != "PDFA" || job.GetContentType() != "application/pdf" {
		t.Fatalf("payload=%q type=%q", job.GetPayload(), job.GetContentType())
	}
	attrs := job.GetAttributes()
	if attrs["datasource.source_id"] != source.ID || attrs["upload.bucket"] != "docs" ||
		attrs["upload.key"] != "kb/a.pdf" || attrs["upload.etag"] != "etag-a" ||
		attrs["datasource.target_collection"] != "wiki" {
		t.Fatalf("attributes = %v", attrs)
	}
	if producer.jobs[0].GetIdempotencyKey() == producer.jobs[1].GetIdempotencyKey() {
		t.Fatal("idempotency keys collided across objects")
	}
}

// A permission/transport failure on an object must abort the sync, not be skipped
// so that a wrong secret key reads as an empty bucket (successful zero-doc sync).
func TestRunDatasourceSync_UploadPropagatesFetchFailure(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	fake := &fakeUploadClient{
		entries:  []objectstore.Entry{{Key: "kb/a.pdf", ETag: "e"}},
		fetchErr: map[string]error{"kb/a.pdf": errors.New("AccessDenied")},
	}
	svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
	source := newUploadSource(t, svc)

	if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err == nil {
		t.Fatal("a 403 on every object must fail the sync, not read as an empty bucket")
	}
}

// A listed object deleted before its fetch (404) is skipped best-effort while the
// rest still deliver; an oversized object is surfaced as an error.
func TestRunDatasourceSync_UploadSkipsVanishedAndSurfacesOversized(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	fake := &fakeUploadClient{
		entries: []objectstore.Entry{{Key: "ok.pdf", ETag: "e1"}, {Key: "gone.pdf", ETag: "e2"}, {Key: "huge.pdf", ETag: "e3"}},
		objects: map[string]objectstore.Object{"ok.pdf": {Key: "ok.pdf", Body: []byte("OK"), ETag: "e1"}},
		fetchErr: map[string]error{
			"gone.pdf": objectstore.ErrObjectNotFound,
			"huge.pdf": objectstore.ErrObjectTooLarge,
		},
	}
	svc.SetDatasourceUploadClientFactory(func(business.UploadDatasourceConfig, string) business.UploadContentClient { return fake })
	source := newUploadSource(t, svc)

	enqueued, err := svc.RunDatasourceSync(context.Background(), source.ID)
	if err == nil {
		t.Fatal("the oversized object must surface as an error")
	}
	// The vanished object is skipped and the deliverable one still enqueues.
	if enqueued != 1 || len(producer.jobs) != 1 || producer.jobs[0].GetAttributes()["upload.key"] != "ok.pdf" {
		t.Fatalf("enqueued=%d jobs=%d, want only ok.pdf delivered", enqueued, len(producer.jobs))
	}
}

func TestRunDatasourceSync_APIEnqueuesFetchedBody(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	fake := &fakeAPIClient{result: &apisource.Result{Body: []byte(`{"x":1}`), ContentType: "application/json"}}
	svc.SetDatasourceAPIClientFactory(func(business.APIDatasourceConfig, string) business.APIContentClient { return fake })

	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderAPI, TargetCollection: "wiki",
		Credential: "sekret", API: apiConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}

	enqueued, err := svc.RunDatasourceSync(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 || len(producer.jobs) != 1 || fake.calls != 1 {
		t.Fatalf("enqueued=%d jobs=%d fetch calls=%d, want 1/1/1", enqueued, len(producer.jobs), fake.calls)
	}
	job := producer.jobs[0]
	if job.GetTopic() != "datasource.api.sync" || !job.GetScope().GetGlobal() {
		t.Fatalf("topic=%q scope=%v", job.GetTopic(), job.GetScope())
	}
	if string(job.GetPayload()) != `{"x":1}` || job.GetContentType() != "application/json" {
		t.Fatalf("payload=%q type=%q", job.GetPayload(), job.GetContentType())
	}
	attrs := job.GetAttributes()
	if attrs["datasource.source_id"] != source.ID || attrs["datasource.org_id"] != testOrg ||
		attrs["datasource.target_collection"] != "wiki" || attrs["api.url"] != "https://api.example.com/v1/docs" ||
		attrs["api.content_sha"] == "" {
		t.Fatalf("attributes = %v", attrs)
	}
}
