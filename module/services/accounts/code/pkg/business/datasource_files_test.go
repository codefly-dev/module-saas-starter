package business_test

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // G505: a git object id is SHA-1 by definition
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const filesVersion = "0123456789abcdef0123456789abcdef01234567"

// blobID names content the way git does, so the files a test serves carry real
// item ids.
func blobID(content []byte) string {
	h := sha1.New() //nolint:gosec // G401: git object ids are SHA-1
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// docsTree is a repository of n markdown files under docs/ plus two files
// outside the source's scope.
func docsTree(n int) (*fakeGitHub, map[string][]byte) {
	gh := &fakeGitHub{defaultBranch: "main", commit: filesVersion, content: map[string][]byte{}}
	byPath := map[string][]byte{}
	add := func(path string, content []byte) {
		gh.files = append(gh.files, github.File{Path: path, SHA: blobID(content), Size: -1})
		gh.content[path] = content
		byPath[path] = content
	}
	for i := range n {
		add(fmt.Sprintf("docs/page-%03d.md", i), []byte(fmt.Sprintf("# Page %d\n\nbody of page %d\n", i, i)))
	}
	add("src/main.go", []byte("package main\n"))
	add("docs/diagram.png", []byte("\x89PNG not markdown"))
	return gh, byPath
}

// A snapshot of many files fetches its content in one batch, and the manifest
// carries each file's true size read from the mirror.
func TestSnapshotFetchesItsContentInOneBatch(t *testing.T) {
	gh, byPath := docsTree(45)
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", Paths: []string{"docs"},
		FileExtensions: []string{".md"}, CollectionLabel: "guides", AccessToken: "t",
	})

	enqueued, err := svc.ReconcileGitHubSource(context.Background(), source, true, "job-1")
	if err != nil || !enqueued {
		t.Fatalf("reconcile = %v, %v", enqueued, err)
	}
	if got := gh.fetches(); got != 1 {
		t.Fatalf("a 45-file snapshot made %d content fetches, want 1", got)
	}
	if len(producer.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want the one manifest", len(producer.jobs))
	}
	var manifest struct {
		Commit string `json:"commit"`
		Files  []struct {
			Path    string `json:"path"`
			BlobSHA string `json:"blob_sha"`
			Size    int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal(producer.jobs[0].GetPayload(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Commit != filesVersion || len(manifest.Files) != 45 {
		t.Fatalf("manifest names %s with %d files, want %s with 45", manifest.Commit, len(manifest.Files), filesVersion)
	}
	for _, f := range manifest.Files {
		if want := int64(len(byPath[f.Path])); f.Size != want || f.BlobSHA != blobID(byPath[f.Path]) {
			t.Fatalf("%s = %s/%d, want %s/%d", f.Path, f.BlobSHA, f.Size, blobID(byPath[f.Path]), want)
		}
	}
	// The mirror is the source's own, named by its org and id.
	if len(gh.workspaces) == 0 || gh.workspaces[0] != (github.Workspace{Org: source.OrgID, Source: source.ID}) {
		t.Fatalf("mirror workspace = %v, want the source's own", gh.workspaces)
	}
}

// An incremental change set fetches all of its changed content in one batch.
func TestIncrementalChangeSetFetchesItsContentInOneBatch(t *testing.T) {
	gh := &fakeGitHub{content: map[string][]byte{}}
	var changed []github.ChangedFile
	for i := range 12 {
		path := fmt.Sprintf("docs/changed-%02d.md", i)
		gh.content[path] = []byte(path)
		changed = append(changed, github.ChangedFile{Filename: path, Status: "modified", SHA: fmt.Sprintf("s%02d", i)})
	}
	changed = append(changed, github.ChangedFile{Filename: "docs/gone.md", Status: "removed"})
	gh.compareFn = compareBetween(cA, cB, changed)
	gh.commit = cB
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := githubSource(t, svc, "main", []string{"docs"}, cA)

	if _, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", cA, cB, false, false), "d1"); err != nil {
		t.Fatal(err)
	}
	if got := gh.fetches(); got != 1 {
		t.Fatalf("a 12-file change set made %d content fetches, want 1", got)
	}
	if len(producer.jobs) != 13 {
		t.Fatalf("enqueued %d ops, want 13", len(producer.jobs))
	}
}

// A rate limit that names its reset holds the job until then, instead of
// retrying into the same wall on the queue's schedule.
func TestRateLimitedSyncWaitsForTheReset(t *testing.T) {
	gh, _ := docsTree(3)
	reset := time.Now().Add(40 * time.Minute).UTC().Truncate(time.Second)
	gh.fetchErr = &github.RateLimitError{ResetAt: reset, Unauthenticated: true}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, gh)
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "guides", AccessToken: "t",
	})

	err := svc.NewDatasourceDeliveryJobHandler()(context.Background(), &jobsv1.JobEnvelope{
		Id:         "job-1",
		Queue:      business.DatasourceDeliveryQueue,
		Topic:      business.DatasourceReconcileTopic,
		Attributes: map[string]string{"datasource.source_id": source.ID, "datasource.reconcile_mode": "force"},
	})
	var failure *jobs.ProcessingError
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want a ProcessingError", err)
	}
	if !failure.Retryable || failure.Failure.GetCode() != "datasource.github_unauthenticated_rate_limited" {
		t.Fatalf("failure = %+v, want the retryable unauthenticated rate limit", failure)
	}
	if !failure.NotBefore.Equal(reset) {
		t.Fatalf("not before = %v, want the reset %v", failure.NotBefore, reset)
	}
	if !strings.Contains(failure.Failure.GetMessage(), reset.Format(time.RFC3339)) {
		t.Fatalf("message %q does not name the reset", failure.Failure.GetMessage())
	}
}

func filesService(t *testing.T, gh *fakeGitHub, queues []string) (*business.Service, *business.DatasourceSource) {
	t.Helper()
	svc, source, _ := filesServiceWithAudit(t, gh, queues)
	return svc, source
}

func filesServiceWithAudit(t *testing.T, gh *fakeGitHub, queues []string) (*business.Service, *business.DatasourceSource, *recordingAudit) {
	t.Helper()
	svc, audit := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, gh)
	svc.SetModuleCapabilities(&fakeJobBackend{}, &fakeJobBackend{}, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: queues, CrossTenant: true},
	})
	source, err := svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Paths: []string{"docs"}, FileExtensions: []string{".md"},
		CollectionLabel: "guides", AccessToken: "ghp_token",
	})
	if err != nil {
		t.Fatalf("AddGitHubSource: %v", err)
	}
	return svc, source, audit
}

type servedFile struct {
	file    business.DatasourceFile
	content []byte
}

func fetchFiles(svc *business.Service, sourceID string, refs []business.DatasourceFileRequest) ([]servedFile, error) {
	var served []servedFile
	err := svc.ModuleFetchDatasourceFiles(context.Background(), moduleCaller(), sourceID, filesVersion, refs,
		func(file business.DatasourceFile, content io.Reader) error {
			b, err := io.ReadAll(content)
			if err != nil {
				return err
			}
			served = append(served, servedFile{file: file, content: b})
			return nil
		})
	return served, err
}

func errorReason(t *testing.T, err error) (codes.Code, *errdetails.ErrorInfo, *errdetails.RetryInfo) {
	t.Helper()
	st := status.Convert(err)
	var info *errdetails.ErrorInfo
	var retry *errdetails.RetryInfo
	for _, d := range st.Details() {
		switch v := d.(type) {
		case *errdetails.ErrorInfo:
			info = v
		case *errdetails.RetryInfo:
			retry = v
		}
	}
	return st.Code(), info, retry
}

func TestModuleFetchDatasourceFiles_ServesTheBatchInOrderWithItsEnvelope(t *testing.T) {
	gh, byPath := docsTree(40)
	svc, source, audit := filesServiceWithAudit(t, gh, []string{"datasource"})
	var refs []business.DatasourceFileRequest
	for i := 39; i >= 0; i-- {
		path := fmt.Sprintf("docs/page-%03d.md", i)
		refs = append(refs, business.DatasourceFileRequest{ItemID: path, ItemVersion: blobID(byPath[path])})
	}

	served, err := fetchFiles(svc, source.ID, refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(served) != 40 {
		t.Fatalf("served %d files, want 40", len(served))
	}
	if got := gh.fetches(); got != 1 {
		t.Fatalf("a 40-file batch made %d fetches, want 1", got)
	}
	// One access event for the batch, carrying what it served — never one per file.
	fetched := audit.entriesOf(business.EventDatasourceFilesFetched)
	if len(fetched) != 1 || len(audit.entriesOf(business.EventDatasourceBlobFetched)) != 0 {
		t.Fatalf("audit = %v, want exactly one files_fetched", audit.types())
	}
	var total int64
	for _, s := range served {
		total += s.file.Size
	}
	if p := fetched[0].Payload; p["files"] != 40 || p["bytes"] != total || p["version"] != filesVersion || fetched[0].OrgID != source.OrgID {
		t.Fatalf("files_fetched payload = %v on org %s, want 40 files, %d bytes at %s on %s", p, fetched[0].OrgID, total, filesVersion, source.OrgID)
	}
	requireDeclaredPayloads(t, audit)
	for i, s := range served {
		if s.file.Path != refs[i].ItemID || !bytes.Equal(s.content, byPath[refs[i].ItemID]) || s.file.Size != int64(len(s.content)) {
			t.Fatalf("file %d = %s (%d bytes), want %s in request order", i, s.file.Path, len(s.content), refs[i].ItemID)
		}
		p := s.file.Provenance
		if p.SourceID != source.ID || p.OrgID != source.OrgID || p.BoundaryNodeID != source.BoundaryNodeID || p.Version != filesVersion ||
			p.ItemID != refs[i].ItemID || p.ItemVersion != refs[i].ItemVersion {
			t.Fatalf("provenance = %+v, want the source, its org and boundary, the version, the item's path and its blob id", p)
		}
	}
}

// Every named file must be the version's in-scope file under that item id; a
// request naming anything else is refused whole, before any bytes are served.
func TestModuleFetchDatasourceFiles_RefusesWhatTheVersionDoesNotList(t *testing.T) {
	gh, byPath := docsTree(3)
	svc, source := filesService(t, gh, []string{"datasource"})
	good := business.DatasourceFileRequest{ItemID: "docs/page-000.md", ItemVersion: blobID(byPath["docs/page-000.md"])}
	for name, bad := range map[string]business.DatasourceFileRequest{
		"another file's item version": {ItemID: "docs/page-001.md", ItemVersion: blobID(byPath["docs/page-002.md"])},
		"out of path scope":           {ItemID: "src/main.go", ItemVersion: blobID(byPath["src/main.go"])},
		"out of file-type scope":      {ItemID: "docs/diagram.png", ItemVersion: blobID(byPath["docs/diagram.png"])},
		"unknown path":                {ItemID: "docs/missing.md", ItemVersion: blobID([]byte("x"))},
	} {
		t.Run(name, func(t *testing.T) {
			served, err := fetchFiles(svc, source.ID, []business.DatasourceFileRequest{good, bad})
			code, info, _ := errorReason(t, err)
			if code != codes.NotFound || info.GetReason() != business.DatasourceReasonFileNotInVersion {
				t.Fatalf("err = %v, want NotFound %s", err, business.DatasourceReasonFileNotInVersion)
			}
			if len(served) != 0 {
				t.Fatalf("served %d files before refusing the batch", len(served))
			}
		})
	}
}

func TestModuleFetchDatasourceFiles_LimitsFailClosed(t *testing.T) {
	gh := &fakeGitHub{content: map[string][]byte{}}
	var refs []business.DatasourceFileRequest
	for i := range 3 {
		content := bytes.Repeat([]byte{byte('a' + i)}, 22<<20)
		path := fmt.Sprintf("docs/big-%d.md", i)
		gh.files = append(gh.files, github.File{Path: path, SHA: blobID(content)})
		gh.content[path] = content
		refs = append(refs, business.DatasourceFileRequest{ItemID: path, ItemVersion: blobID(content)})
	}
	huge := bytes.Repeat([]byte("z"), github.MaxFileBytes+1)
	gh.files = append(gh.files, github.File{Path: "docs/huge.md", SHA: blobID(huge)})
	gh.content["docs/huge.md"] = huge
	svc, source := filesService(t, gh, []string{"datasource"})

	_, err := fetchFiles(svc, source.ID, refs)
	if code, info, _ := errorReason(t, err); code != codes.FailedPrecondition || info.GetReason() != business.DatasourceReasonBatchTooLarge {
		t.Fatalf("66 MiB batch err = %v, want %s", err, business.DatasourceReasonBatchTooLarge)
	}
	_, err = fetchFiles(svc, source.ID, []business.DatasourceFileRequest{{ItemID: "docs/huge.md", ItemVersion: blobID(huge)}})
	if code, info, _ := errorReason(t, err); code != codes.FailedPrecondition || info.GetReason() != business.DatasourceReasonFileTooLarge {
		t.Fatalf("oversized file err = %v, want %s", err, business.DatasourceReasonFileTooLarge)
	}
	tooMany := make([]business.DatasourceFileRequest, 1001)
	for i := range tooMany {
		tooMany[i] = business.DatasourceFileRequest{ItemID: fmt.Sprintf("docs/%d.md", i), ItemVersion: blobID([]byte{byte(i)})}
	}
	if _, err := fetchFiles(svc, source.ID, tooMany); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("1001-file batch err = %v, want InvalidArgument", err)
	}
}

func TestModuleFetchDatasourceFiles_RateLimitIsTypedWithItsReset(t *testing.T) {
	gh, byPath := docsTree(2)
	reset := time.Now().Add(25 * time.Minute).UTC().Truncate(time.Second)
	gh.fetchErr = &github.RateLimitError{ResetAt: reset}
	svc, source := filesService(t, gh, []string{"datasource"})

	_, err := fetchFiles(svc, source.ID, []business.DatasourceFileRequest{{ItemID: "docs/page-000.md", ItemVersion: blobID(byPath["docs/page-000.md"])}})
	code, info, retry := errorReason(t, err)
	if code != codes.ResourceExhausted || info.GetReason() != business.DatasourceReasonRateLimited {
		t.Fatalf("err = %v, want ResourceExhausted %s", err, business.DatasourceReasonRateLimited)
	}
	if info.GetMetadata()["reset_at"] != reset.Format(time.RFC3339) {
		t.Fatalf("reset_at = %q, want %s", info.GetMetadata()["reset_at"], reset.Format(time.RFC3339))
	}
	if d := retry.GetRetryDelay().AsDuration(); d < 24*time.Minute || d > 25*time.Minute {
		t.Fatalf("retry delay = %v, want the time to the reset", d)
	}
}

func TestModuleFetchDatasourceFiles_RequiresTheDatasourceQueueGrant(t *testing.T) {
	gh, byPath := docsTree(1)
	svc, source := filesService(t, gh, []string{"other"})
	_, err := fetchFiles(svc, source.ID, []business.DatasourceFileRequest{{ItemID: "docs/page-000.md", ItemVersion: blobID(byPath["docs/page-000.md"])}})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if gh.fetches() != 0 {
		t.Fatal("an unauthorized caller reached the repository")
	}
}
