package generatedgo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFormatRootsFormatsNestedGeneratedFilesAndAllowsAbsentContributionRoot(t *testing.T) {
	root := t.TempDir()
	generated := filepath.Join(root, "saas", "composed", "settings", "v1")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(generated, "settings.pb.go")
	input := "package settingsv1\n\nimport (\n\tprotoimpl \"google.golang.org/protobuf/runtime/protoimpl\"\n\treflect \"reflect\"\n)\n\nvar _ = reflect.TypeOf\nvar _ = protoimpl.UnsafeEnabled\n"
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := FormatRoots(root, filepath.Join(root, "contributed")); err != nil {
		t.Fatal(err)
	}

	want := "package settingsv1\n\nimport (\n\treflect \"reflect\"\n\n\tprotoimpl \"google.golang.org/protobuf/runtime/protoimpl\"\n)\n\nvar _ = reflect.TypeOf\nvar _ = protoimpl.UnsafeEnabled\n"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("formatted source mismatch:\n%s", got)
	}
}
