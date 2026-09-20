package business_test

import (
	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	"context"
	"testing"
)

func TestFileExtensionsSnapshotExcludesUnselectedFiles(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{commit: "HEAD", files: []github.File{
		{Path: "README.md", SHA: "a"}, {Path: "docs/guide.MD", SHA: "b"}, {Path: "image.png", SHA: "c"},
	}}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "Example collection", AccessToken: "t", FileExtensions: []string{".MD"},
	})
	if len(source.FileExtensions) != 1 || source.FileExtensions[0] != ".md" {
		t.Fatalf("filter not retained: %v", source.FileExtensions)
	}
	if _, err := svc.ReconcileGitHubSource(context.Background(), source, true, "request"); err != nil {
		t.Fatal(err)
	}
	if len(producer.jobs) != 1 {
		t.Fatalf("jobs: %d", len(producer.jobs))
	}
	manifest := decodeChangeSetFile(t, producer.jobs[0])
	files := manifest["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("expected only selected files: %v", files)
	}
	for _, raw := range files {
		if raw.(map[string]any)["path"] == "image.png" {
			t.Fatal("unselected file entered ingestion")
		}
	}
}

// The suffix intersection has to hold at the enqueued job, not only in the op
// compiler: a push carrying an unselected file, a rename out of the selection
// and a rename into it must reach ingestion as exactly the two ops the filter
// admits, with the rename out expressed as a delete of the old path.
func TestFileExtensionsIncrementalEnqueuesOnlySelectedFiles(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{
		// Every path the push touches is present, so a compiler that stopped
		// applying the filter fails on the op count rather than a content fetch.
		content: map[string][]byte{
			"docs/guide.md": []byte("G"), "docs/logo.png": []byte("P"), "docs/notes.txt": []byte("N"),
		},
		compareFn: compareBetween("A", "B", []github.ChangedFile{
			{Filename: "docs/logo.png", Status: "modified", SHA: "sp"},
			{Filename: "docs/notes.txt", PreviousFilename: "docs/notes.md", Status: "renamed", SHA: "sn"},
			{Filename: "docs/guide.md", PreviousFilename: "docs/draft.txt", Status: "renamed", SHA: "sg"},
		}),
	}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", Paths: []string{"docs"},
		CollectionLabel: "guides", AccessToken: "t", FileExtensions: []string{".md"},
	})
	source.LastIngestedCommit = "A"

	disp, err := svc.CompileGitHubDelivery(context.Background(), source,
		pushDelivery("refs/heads/main", "A", "B", false, false), "d1")
	if err != nil {
		t.Fatal(err)
	}
	if disp != business.DispositionCompiled {
		t.Fatalf("disposition = %q, want compiled", disp)
	}
	if len(producer.jobs) != 2 {
		t.Fatalf("enqueued %d jobs, want 2", len(producer.jobs))
	}
	got := map[string]string{}
	for _, job := range producer.jobs {
		file := decodeChangeSetFile(t, job)
		got[file["path"].(string)] = file["change_type"].(string)
	}
	want := map[string]string{"docs/guide.md": "added", "docs/notes.md": "removed"}
	for path, change := range want {
		if got[path] != change {
			t.Fatalf("op for %s = %q, want %q (all ops: %v)", path, got[path], change, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("unselected file entered ingestion: %v", got)
	}
}

// The provider-agnostic create path normalizes the allowlist the same way the
// GitHub-specific one does; a source connected through it must not reach the
// compiler carrying raw operator input.
func TestAddSourceNormalizesFileExtensions(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderGitHub, Repo: "acme/docs",
		CollectionLabel: "guides", Credential: "t", FileExtensions: []string{" .MD ", ".md", ".mdx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.FileExtensions) != 2 || source.FileExtensions[0] != ".md" || source.FileExtensions[1] != ".mdx" {
		t.Fatalf("file extensions = %v", source.FileExtensions)
	}
	if _, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderGitHub, Repo: "acme/docs",
		CollectionLabel: "guides", Credential: "t", FileExtensions: []string{"**/*.md"},
	}); err == nil {
		t.Fatal("accepted a glob as a file extension")
	}
}
