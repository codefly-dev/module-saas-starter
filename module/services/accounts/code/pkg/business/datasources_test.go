package business_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
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
