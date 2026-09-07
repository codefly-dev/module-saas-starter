package business_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

// pushDelivery builds a raw GitHub push payload the compiler parses.
func pushDelivery(ref, before, after string, created, deleted bool) []byte {
	body, _ := json.Marshal(map[string]any{
		"ref": ref, "before": before, "after": after, "created": created, "deleted": deleted,
	})
	return body
}

// githubSource registers a GitHub source and returns it with its cursor set to
// the given commit (empty means never ingested).
func githubSource(t *testing.T, svc *business.Service, branch string, paths []string, cursor string) *business.DatasourceSource {
	t.Helper()
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: branch, Paths: paths, CollectionLabel: "wiki", AccessToken: "t",
	})
	source.LastIngestedCommit = cursor
	return source
}

func decodeChangeSetFile(t *testing.T, job *jobsv1.NewJob) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(job.GetPayload(), &out); err != nil {
		t.Fatalf("decode change-set payload: %v", err)
	}
	return out
}

// compareBetween returns a compareFn that answers the ancestry probe
// (after...cursor) as "behind" (not an ancestor) and the incremental diff
// (cursor...after) with the given files.
func compareBetween(cursor, after string, files []github.ChangedFile) func(base, head string) (*github.Comparison, error) {
	return func(base, head string) (*github.Comparison, error) {
		if base == after && head == cursor {
			return &github.Comparison{Status: github.CompareStatusBehind}, nil
		}
		if base == cursor && head == after {
			return &github.Comparison{Status: github.CompareStatusAhead, Files: files}, nil
		}
		return nil, errors.New("unexpected compare " + base + "..." + head)
	}
}

func setStoredCursor(store *datasourceFakeStore, id, commit string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if s, ok := store.sources[id]; ok {
		s.LastIngestedCommit = commit
	}
}

func storedCursor(t *testing.T, store *datasourceFakeStore, id string) string {
	t.Helper()
	source, err := store.GetDatasourceSourceByID(context.Background(), id)
	if err != nil || source == nil {
		t.Fatalf("load stored source: %v", err)
	}
	return source.LastIngestedCommit
}

func setNextReconcile(t *testing.T, store *datasourceFakeStore, id string, at time.Time) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	source, ok := store.sources[id]
	if !ok {
		t.Fatalf("source %q not found", id)
	}
	if at.IsZero() {
		source.NextReconcileAt = nil
		return
	}
	next := at
	source.NextReconcileAt = &next
}

func auditHas(audit *recordingAudit, event business.EventType) bool {
	for _, e := range audit.types() {
		if e == event {
			return true
		}
	}
	return false
}

func TestCompileDelivery_BranchFilterDrops(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, &fakeGitHub{})
	source := githubSource(t, svc, "main", nil, "")

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/feature/x", "b", "a", false, false), "d1")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionIgnoredBranch {
		t.Fatalf("disposition = %q, want ignored_branch", disp)
	}
	if len(producer.jobs) != 0 {
		t.Fatalf("enqueued %d jobs, want 0", len(producer.jobs))
	}
}

func TestCompileDelivery_PathFilterAndRenames(t *testing.T) {
	cases := []struct {
		name       string
		files      []github.ChangedFile
		wantPath   string
		wantChange string
	}{
		{
			name: "only in-scope modified file",
			files: []github.ChangedFile{
				{Filename: "docs/a.md", Status: "modified", SHA: "sa"},
				{Filename: "src/b.go", Status: "modified", SHA: "sb"},
			},
			wantPath: "docs/a.md", wantChange: "modified",
		},
		{
			name:     "rename out of scope is a delete of the old path",
			files:    []github.ChangedFile{{Filename: "src/a.md", PreviousFilename: "docs/a.md", Status: "renamed", SHA: "sr"}},
			wantPath: "docs/a.md", wantChange: "removed",
		},
		{
			name:     "rename into scope is an upsert of the new path",
			files:    []github.ChangedFile{{Filename: "docs/a.md", PreviousFilename: "src/a.md", Status: "renamed", SHA: "sr"}},
			wantPath: "docs/a.md", wantChange: "added",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			producer := &recordingProducer{}
			gh := &fakeGitHub{
				content:   map[string][]byte{"docs/a.md": []byte("A")},
				compareFn: compareBetween("A", "B", tc.files),
			}
			svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
			source := githubSource(t, svc, "main", []string{"docs"}, "A")

			disp, err := svc.CompileGitHubDelivery(context.Background(), source,
				pushDelivery("refs/heads/main", "A", "B", false, false), "d1")
			if err != nil {
				t.Fatal(err)
			}
			if disp != business.DispositionCompiled {
				t.Fatalf("disposition = %q, want compiled", disp)
			}
			if len(producer.jobs) != 1 {
				t.Fatalf("enqueued %d jobs, want exactly 1 in-scope op", len(producer.jobs))
			}
			file := decodeChangeSetFile(t, producer.jobs[0])
			if file["path"] != tc.wantPath || file["change_type"] != tc.wantChange {
				t.Fatalf("op = path %v change %v, want %s/%s", file["path"], file["change_type"], tc.wantPath, tc.wantChange)
			}
		})
	}
}

func TestCompileDelivery_OutOfOrderDropsStale(t *testing.T) {
	producer := &recordingProducer{}
	// Cursor already at C. A late delivery for A→B arrives: B is an ancestor of C
	// (B...C is ahead), so it is dropped and the cursor stays at C.
	gh := &fakeGitHub{compareFn: func(base, head string) (*github.Comparison, error) {
		if base == "B" && head == "C" {
			return &github.Comparison{Status: github.CompareStatusAhead}, nil
		}
		return nil, errors.New("unexpected compare " + base + "..." + head)
	}}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, gh)
	source := githubSource(t, svc, "main", nil, "C")
	setStoredCursor(store, source.ID, "C")

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "A", "B", false, false), "late")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionStale {
		t.Fatalf("disposition = %q, want stale", disp)
	}
	if len(producer.jobs) != 0 {
		t.Fatalf("enqueued %d jobs, want 0 for a stale delivery", len(producer.jobs))
	}
	if got := storedCursor(t, store, source.ID); got != "C" {
		t.Fatalf("cursor moved to %q, want it to stay at C", got)
	}
}

func TestCompileDelivery_BehindCompareNeverRewindsCursor(t *testing.T) {
	producer := &recordingProducer{}
	// A redelivered older push (after=B, an ancestor of the cursor C) arrives while
	// the ancestry pre-check Compare(B...C) transiently fails. The main compare
	// C...B then reports "behind"; compiling it would rewind the cursor to B and
	// re-emit reverted content, so it must be dropped as stale with the cursor
	// left at C.
	gh := &fakeGitHub{compareFn: func(base, head string) (*github.Comparison, error) {
		if base == "B" && head == "C" {
			return nil, errors.New("transient GitHub error")
		}
		if base == "C" && head == "B" {
			return &github.Comparison{Status: github.CompareStatusBehind}, nil
		}
		return nil, errors.New("unexpected compare " + base + "..." + head)
	}}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, gh)
	source := githubSource(t, svc, "main", nil, "C")
	setStoredCursor(store, source.ID, "C")

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "A", "B", false, false), "redelivery")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionStale {
		t.Fatalf("disposition = %q, want stale for a behind compare", disp)
	}
	if len(producer.jobs) != 0 {
		t.Fatalf("enqueued %d jobs, want 0 for a behind (backward) delivery", len(producer.jobs))
	}
	if got := storedCursor(t, store, source.ID); got != "C" {
		t.Fatalf("cursor rewound to %q, want it to stay at C", got)
	}
}

func TestCompileDelivery_MissedDeliveryDiffsFromCursorNotBefore(t *testing.T) {
	producer := &recordingProducer{}
	var comparedBase string
	// Cursor at A; the B→C delivery arrives but A→B was lost. The diff must run
	// from the cursor (A), not the delivery's own `before` (B), so B's changes are
	// caught up.
	gh := &fakeGitHub{
		content: map[string][]byte{"docs/from-b.md": []byte("B"), "docs/from-c.md": []byte("C")},
		compareFn: func(base, head string) (*github.Comparison, error) {
			if base == "C" && head == "A" { // ancestry check: not an ancestor
				return &github.Comparison{Status: github.CompareStatusBehind}, nil
			}
			if head == "C" {
				comparedBase = base
				return &github.Comparison{Status: github.CompareStatusAhead, Files: []github.ChangedFile{
					{Filename: "docs/from-b.md", Status: "added", SHA: "b"},
					{Filename: "docs/from-c.md", Status: "added", SHA: "c"},
				}}, nil
			}
			return nil, errors.New("unexpected compare " + base + "..." + head)
		},
	}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := githubSource(t, svc, "main", []string{"docs"}, "A")

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "B", "C", false, false), "d")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionCompiled {
		t.Fatalf("disposition = %q, want compiled", disp)
	}
	if comparedBase != "A" {
		t.Fatalf("diffed from %q, want the cursor A", comparedBase)
	}
	if len(producer.jobs) != 2 {
		t.Fatalf("enqueued %d jobs, want both B's and C's files", len(producer.jobs))
	}
}

func TestCompileDelivery_ForcePushSnapshots(t *testing.T) {
	producer := &recordingProducer{}
	// `before` (or the cursor A) is unreachable: the incremental compare 404s, so
	// the delivery snapshots at the head and the cursor moves to the head.
	gh := &fakeGitHub{
		files: []github.File{{Path: "docs/a.md", SHA: "sa", Size: 3}},
		compareFn: func(base, head string) (*github.Comparison, error) {
			if base == "C" && head == "A" {
				return &github.Comparison{Status: github.CompareStatusDiverged}, nil
			}
			if base == "A" && head == "C" {
				return nil, github.ErrNotFound
			}
			return nil, errors.New("unexpected compare " + base + "..." + head)
		},
	}
	store := newDatasourceFakeStore()
	svc, audit := newDatasourceService(store, producer, gh)
	source := githubSource(t, svc, "main", []string{"docs"}, "A")

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "X", "C", false, false), "force")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionSnapshot {
		t.Fatalf("disposition = %q, want snapshot", disp)
	}
	if len(producer.jobs) != 1 || producer.jobs[0].GetTopic() != "datasource.github.snapshot" {
		t.Fatalf("want exactly one snapshot job, got %d", len(producer.jobs))
	}
	if got := storedCursor(t, store, source.ID); got != "C" {
		t.Fatalf("cursor = %q, want C after snapshot", got)
	}
	if !auditHas(audit, business.EventDatasourceForcePushReconciled) {
		t.Fatalf("audit = %v, want force_push_reconciled", audit.types())
	}
}

func TestCompileDelivery_CursorMonotonicRejectsAncestor(t *testing.T) {
	producer := &recordingProducer{}
	// after (B) is an ancestor of the cursor (C): B...C ahead ⇒ drop as stale and
	// never move the cursor backwards.
	gh := &fakeGitHub{compareFn: func(base, head string) (*github.Comparison, error) {
		if base == "B" && head == "C" {
			return &github.Comparison{Status: github.CompareStatusAhead}, nil
		}
		return nil, errors.New("unexpected compare " + base + "..." + head)
	}}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, gh)
	source := githubSource(t, svc, "main", nil, "C")
	setStoredCursor(store, source.ID, "C")

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "A", "B", false, false), "d")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionStale {
		t.Fatalf("disposition = %q, want stale", disp)
	}
	if got := storedCursor(t, store, source.ID); got != "C" {
		t.Fatalf("cursor = %q, want it to stay at C (monotonic)", got)
	}
}

func TestCompileDelivery_CreatedBranchSnapshots(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{files: []github.File{{Path: "docs/a.md", SHA: "sa", Size: 1}}}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, gh)
	source := githubSource(t, svc, "main", []string{"docs"}, "")

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "0000000000000000000000000000000000000000", "C", true, false), "d")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionSnapshot {
		t.Fatalf("disposition = %q, want snapshot for a created branch", disp)
	}
	if got := storedCursor(t, store, source.ID); got != "C" {
		t.Fatalf("cursor = %q, want C", got)
	}
}

func TestCompileDelivery_DeletedBranchKeepsDocuments(t *testing.T) {
	producer := &recordingProducer{}
	svc, audit := newDatasourceService(newDatasourceFakeStore(), producer, &fakeGitHub{})
	source := githubSource(t, svc, "main", nil, "A")

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "A", "0000000000000000000000000000000000000000", false, true), "d")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionBranchDeleted {
		t.Fatalf("disposition = %q, want branch_deleted", disp)
	}
	if len(producer.jobs) != 0 {
		t.Fatalf("a branch deletion must not enqueue document ops, got %d", len(producer.jobs))
	}
	if !auditHas(audit, business.EventDatasourceBranchDeleted) {
		t.Fatalf("audit = %v, want branch_deleted", audit.types())
	}
}

func TestCompileDelivery_LargeBlobCarriesContentTicket(t *testing.T) {
	producer := &recordingProducer{}
	blob := bytes.Repeat([]byte("x"), 2*1024*1024)
	gh := &fakeGitHub{
		errs:      map[string]error{"docs/big.md": github.ErrFileTooLarge},
		blobs:     map[string][]byte{"bigsha": blob},
		compareFn: compareBetween("A", "B", []github.ChangedFile{{Filename: "docs/big.md", Status: "modified", SHA: "bigsha"}}),
	}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, gh)
	svc.SetDatasourceTicketKey([]byte("test-key"))
	source := githubSource(t, svc, "main", []string{"docs"}, "A")

	if _, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "A", "B", false, false), "d"); err != nil {
		t.Fatal(err)
	}
	if len(producer.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(producer.jobs))
	}
	file := decodeChangeSetFile(t, producer.jobs[0])
	ticket, _ := file["content_ticket"].(string)
	if ticket == "" {
		t.Fatalf("oversized blob must carry a content ticket, got %v", file)
	}
	if _, hasContent := file["content"]; hasContent {
		t.Fatalf("oversized blob must omit inline content, got %v", file)
	}

	// The ticket redeems the blob through accounts, which re-fetches it with the
	// source's own token.
	content, err := svc.ResolveContentTicket(context.Background(), ticket)
	if err != nil {
		t.Fatalf("ResolveContentTicket = %v", err)
	}
	if !bytes.Equal(content, blob) {
		t.Fatalf("resolved %d bytes, want %d", len(content), len(blob))
	}

	// A forged/foreign ticket is refused.
	if _, err := svc.ResolveContentTicket(context.Background(), "garbage"); err == nil {
		t.Fatal("a malformed ticket must be refused")
	}
}

func TestCompileDelivery_CarriesSourceTokenOnEveryCall(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{
		content:   map[string][]byte{"docs/a.md": []byte("A")},
		compareFn: compareBetween("A", "B", []github.ChangedFile{{Filename: "docs/a.md", Status: "modified", SHA: "sa"}}),
	}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	var tokens []string
	svc.SetDatasourceGitHubClientFactory(func(token string) business.GitHubContentClient {
		tokens = append(tokens, token)
		return gh
	})
	source := githubSource(t, svc, "main", []string{"docs"}, "A")

	if _, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "A", "B", false, false), "d"); err != nil {
		t.Fatal(err)
	}
	if len(tokens) == 0 {
		t.Fatal("compiler built no GitHub client")
	}
	for _, tok := range tokens {
		if tok != "t" {
			t.Fatalf("client built with token %q, want the decrypted source token", tok)
		}
	}
}

func TestReconcile_SnapshotsOnlyWhenHeadMoved(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{commit: "HEAD", files: []github.File{{Path: "docs/a.md", SHA: "sa"}}}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)

	// head == cursor: nothing to do.
	source := githubSource(t, svc, "main", []string{"docs"}, "HEAD")
	enqueued, err := svc.ReconcileGitHubSource(context.Background(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued || len(producer.jobs) != 0 {
		t.Fatalf("head==cursor must enqueue nothing, got %d jobs", len(producer.jobs))
	}

	// head != cursor: one snapshot.
	source.LastIngestedCommit = "OLD"
	enqueued, err = svc.ReconcileGitHubSource(context.Background(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	if !enqueued || len(producer.jobs) != 1 || producer.jobs[0].GetTopic() != "datasource.github.snapshot" {
		t.Fatalf("head!=cursor must enqueue one snapshot, got %d jobs", len(producer.jobs))
	}
}

func TestRunDatasourceReconcile_SchedulesDueSourcesOnly(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{})

	due := githubSource(t, svc, "main", nil, "")
	disabled := githubSource(t, svc, "main", nil, "")
	setNextReconcile(t, store, due.ID, time.Now().Add(-time.Minute))
	setNextReconcile(t, store, disabled.ID, time.Time{}) // nil = reconcile disabled

	n, err := svc.RunDatasourceReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(producer.jobs) != 1 {
		t.Fatalf("enqueued %d reconciles, want exactly the due source", len(producer.jobs))
	}
	job := producer.jobs[0]
	if job.GetQueue() != business.DatasourceDeliveryQueue || job.GetTopic() != "datasource.github.reconcile" {
		t.Fatalf("reconcile job routing = %s/%s", job.GetQueue(), job.GetTopic())
	}
	if job.GetAttributes()["datasource.source_id"] != due.ID {
		t.Fatalf("reconcile enqueued for %q, want the due source %q", job.GetAttributes()["datasource.source_id"], due.ID)
	}
}
