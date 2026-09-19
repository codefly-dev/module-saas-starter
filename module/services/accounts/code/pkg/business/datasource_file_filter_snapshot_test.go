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
