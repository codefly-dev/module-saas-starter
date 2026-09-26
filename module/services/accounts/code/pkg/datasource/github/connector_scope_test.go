package github

import (
	"strings"
	"testing"

	"accounts/pkg/datasource/connector"
)

// TestChangesForRenameCrossesTheScope holds the connector to the host's scope
// on an incremental diff: a rename is a move only when both ends are in scope,
// a rename into scope is an addition of the new path, a rename out of it a
// deletion of the old one, and an out-of-scope change is no change at all.
func TestChangesForRenameCrossesTheScope(t *testing.T) {
	inScope := func(p string) bool {
		return strings.HasPrefix(p, "docs/") && strings.HasSuffix(strings.ToLower(p), ".md")
	}
	src := connector.Source{ID: "s", OrgID: "o", BoundaryNodeID: "b"}
	var got []connector.Change
	for _, f := range []ChangedFile{
		{Filename: "docs/in.md", PreviousFilename: "docs/in.txt", Status: "renamed", SHA: "a"},
		{Filename: "docs/out.txt", PreviousFilename: "docs/out.md", Status: "renamed", SHA: "b"},
		{Filename: "docs/keep.MD", PreviousFilename: "docs/old.md", Status: "renamed", SHA: "c"},
		{Filename: "outside/read.md", Status: "added", SHA: "d"},
		{Filename: "docs/image.png", Status: "modified", SHA: "e"},
	} {
		got = append(got, changesFor(src, inScope, f, connector.SourceScoped())...)
	}
	want := []struct {
		id, prev string
		kind     connector.ChangeKind
	}{
		{"docs/in.md", "", connector.ChangeAdded},
		{"docs/out.md", "", connector.ChangeDeleted},
		{"docs/keep.MD", "docs/old.md", connector.ChangeMoved},
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v", got)
	}
	for i, w := range want {
		if got[i].Key.ItemID != w.id || got[i].Kind != w.kind || got[i].PreviousItemID != w.prev {
			t.Fatalf("change %d = %+v, want %+v", i, got[i], w)
		}
	}
}
