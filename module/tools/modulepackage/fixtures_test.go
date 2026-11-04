package modulepackage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncEmbeddedFixturesPreservesBytesAndExactYAMLSet(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "fixtures")
	destination := filepath.Join(root, "services/accounts/code/fixtures/embedded")
	for _, dir := range []string{source, destination} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"example.yaml": "# preserve this comment\nroles: []\n", "added.yaml": "users: []\n"}
	for name, content := range want {
		write(source, name, content)
	}
	write(source, "README.md", "canonical documentation")
	write(destination, "example.yaml", "outdated bytes")
	write(destination, "removed.yaml", "stale generated copy")
	write(destination, "README.md", "preserve unrelated documentation")
	for range 2 {
		if err := SyncEmbeddedFixtures(root); err != nil {
			t.Fatal(err)
		}
		for name, content := range want {
			got, err := os.ReadFile(filepath.Join(destination, name))
			if err != nil || !bytes.Equal(got, []byte(content)) {
				t.Fatalf("copy %s differs: %v", name, err)
			}
		}
		if _, err := os.Stat(filepath.Join(destination, "removed.yaml")); !os.IsNotExist(err) {
			t.Fatalf("stale generated YAML remains: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(destination, "README.md"))
		if err != nil || string(got) != "preserve unrelated documentation" {
			t.Fatalf("non-YAML output was modified: %v", err)
		}
	}
}

func TestSyncEmbeddedFixturesRefusesEmptyOrIndirectInput(t *testing.T) {
	for _, mode := range []string{"empty", "source symlink", "destination symlink"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "fixtures")
			destination := filepath.Join(root, "services/accounts/code/fixtures/embedded")
			for _, dir := range []string{source, destination} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			outside := filepath.Join(root, "unchanged.txt")
			if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
				t.Fatal(err)
			}
			if mode == "source symlink" {
				if err := os.Symlink(outside, filepath.Join(source, "example.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "destination symlink" {
				if err := os.WriteFile(filepath.Join(source, "example.yaml"), []byte("roles: []\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(destination, "example.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			if err := SyncEmbeddedFixtures(root); err == nil {
				t.Fatal("unsafe input was accepted")
			}
			got, err := os.ReadFile(outside)
			if err != nil || string(got) != "untouched" {
				t.Fatalf("refusal modified external file: %v", err)
			}
		})
	}
}
