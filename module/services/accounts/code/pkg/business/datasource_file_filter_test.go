package business

import (
	"accounts/pkg/datasource/github"
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFileExtensionsNormalizeAndValidate(t *testing.T) {
	got, err := normalizeFileExtensions([]string{".MD", " .md ", ".mdx"})
	if err != nil || !reflect.DeepEqual(got, []string{".md", ".mdx"}) {
		t.Fatalf("normalize: %v %v", got, err)
	}
	for _, bad := range []string{"", "md", "**/*.md", "../md", ".md/file", "."} {
		if _, err := normalizeFileExtensions([]string{bad}); err == nil {
			t.Errorf("accepted invalid extension %q", bad)
		} else if status.Code(err) != codes.InvalidArgument {
			t.Errorf("invalid extension reported as %s, want InvalidArgument", status.Code(err))
		}
	}
	for _, name := range []string{"README.md", "docs/README.MD", "a/b/c.md"} {
		if !fileTypeAllowed(name, got) {
			t.Errorf("excluded %q", name)
		}
	}
	for _, name := range []string{"image.png", "README.md.exe", "docs/md"} {
		if fileTypeAllowed(name, got) {
			t.Errorf("included %q", name)
		}
	}
	if !fileTypeAllowed("any.bin", nil) {
		t.Fatal("empty filter must preserve legacy behavior")
	}
}

func TestFileExtensionsIncrementalRenameCrossesFilter(t *testing.T) {
	svc := &Service{}
	ops := svc.changeOps([]github.ChangedFile{
		{Filename: "docs/in.md", PreviousFilename: "docs/in.txt", Status: "renamed", SHA: "a"},
		{Filename: "docs/out.txt", PreviousFilename: "docs/out.md", Status: "renamed", SHA: "b"},
		{Filename: "docs/keep.MD", PreviousFilename: "docs/old.md", Status: "renamed", SHA: "c"},
		{Filename: "outside/read.md", Status: "added", SHA: "d"},
		{Filename: "docs/image.png", Status: "modified", SHA: "e"},
	}, []string{"docs"}, []string{".md"})
	if len(ops) != 3 {
		t.Fatalf("ops: %+v", ops)
	}
	if ops[0].path != "docs/in.md" || ops[0].changeType != changeTypeAdded {
		t.Fatalf("rename in: %+v", ops[0])
	}
	if ops[1].changeType != changeTypeRenamed {
		t.Fatalf("rename within: %+v", ops[1])
	}
	if ops[2].path != "docs/out.md" || ops[2].changeType != changeTypeRemoved {
		t.Fatalf("rename out: %+v", ops[2])
	}
}
