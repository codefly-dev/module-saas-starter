package business

import (
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
