package business_test

import (
	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	"context"
	"slices"
	"testing"
)

// The suffix intersection has to hold at the enqueued job, not only in the op
// compiler: a push carrying an unselected file, a rename out of the selection
// and a rename into it must reach ingestion as exactly the two ops the filter
// admits, with the rename out expressed as a delete of the old path. The
// repository serves every path the push touches, so the filtered-out files are
// absent from the fetch log because the filter excluded them and not because
// the fake had nothing to return.
func TestFileExtensionsIncrementalEnqueuesOnlySelectedFiles(t *testing.T) {
	producer := &recordingProducer{}
	gh := &fakeGitHub{
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
	// The proto documents the filter as applied before content is fetched: an
	// excluded file must cost no blob read, and a delete carries no content.
	if fetched := gh.fetchedPaths(); !slices.Equal(fetched, []string{"docs/guide.md"}) {
		t.Fatalf("fetched %v, want only the one selected upsert", fetched)
	}
}
