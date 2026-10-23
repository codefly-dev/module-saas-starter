package objectstore

import (
	"slices"
	"strings"
	"testing"
)

// TestWindow pins what a sync takes of a listing: the admitted keys in listing
// order, at most the cap, with an excluded key dropped before it is counted.
func TestWindow(t *testing.T) {
	listed := []Entry{{Key: "docs/a.md"}, {Key: "docs/drafts/b.md"}, {Key: "docs/c.md"}, {Key: "docs/d.pdf"}}
	key := func(e Entry) string { return e.Key }
	noDrafts := func(k string) bool { return !strings.HasPrefix(k, "docs/drafts/") }
	for _, tc := range []struct {
		name  string
		scope Scope
		keys  []string
		whole bool
	}{
		{"no filter, within the cap", Scope{MaxObjects: 4}, []string{"docs/a.md", "docs/drafts/b.md", "docs/c.md", "docs/d.pdf"}, true},
		{"the cap cut an admitted key", Scope{MaxObjects: 2}, []string{"docs/a.md", "docs/drafts/b.md"}, false},
		{"an excluded key never uses up the cap", Scope{InScope: noDrafts, MaxObjects: 3}, []string{"docs/a.md", "docs/c.md", "docs/d.pdf"}, true},
		{"the cap counts only admitted keys", Scope{InScope: noDrafts, MaxObjects: 2}, []string{"docs/a.md", "docs/c.md"}, false},
		{"no cap configured is the default", Scope{InScope: noDrafts}, []string{"docs/a.md", "docs/c.md", "docs/d.pdf"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, whole := Window(tc.scope, listed, key)
			var keys []string
			for _, e := range kept {
				keys = append(keys, e.Key)
			}
			if !slices.Equal(keys, tc.keys) || whole != tc.whole {
				t.Fatalf("kept %v whole=%v, want %v whole=%v", keys, whole, tc.keys, tc.whole)
			}
		})
	}
	if got := (Scope{MaxObjects: 5000}).limit(); got != maxObjectsCeiling {
		t.Fatalf("a cap above the ceiling = %d, want %d", got, maxObjectsCeiling)
	}
	if got := (Scope{}).limit(); got != defaultMaxObjects {
		t.Fatalf("no cap = %d, want %d", got, defaultMaxObjects)
	}
}
