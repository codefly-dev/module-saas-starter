package infra

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"accounts/pkg/business"
)

// The mount reader is filesystem-only, so these run without a database. What they
// pin is not "it reads files" but the two rules that make it safe: an unreadable
// mount is never an empty desired set, and a projected ConfigMap volume's
// dot-prefixed shadow directories are not a second copy of every document.

func TestSolutionHostBindingMountReadsDeliveredDocuments(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "acme.prod.crm"+SolutionHostBindingExtension), "binding: acme.prod.crm\n")
	write(t, filepath.Join(root, "acme.prod.pim"+SolutionHostBindingExtension), "binding: acme.prod.pim\n")
	// Not a delivered document: the reader takes the extension the renderer names
	// its data key with, so a README or a checksum beside them is not a document.
	write(t, filepath.Join(root, "README.md"), "not a document")
	write(t, filepath.Join(root, "notes.yaml"), "binding: nope\n")

	documents, err := NewSolutionHostBindingMount(root).Documents(context.Background())
	if err != nil {
		t.Fatalf("read mount: %v", err)
	}
	if len(documents) != 2 {
		t.Fatalf("read %d documents, want 2: %v", len(documents), sources(documents))
	}
	// Ordered by path, so two passes over an unchanged mount read the same set in
	// the same order.
	if !strings.HasSuffix(documents[0].Source, "acme.prod.crm"+SolutionHostBindingExtension) {
		t.Fatalf("documents are not path-ordered: %v", sources(documents))
	}
}

// A Kubernetes projected ConfigMap volume is a directory of symlinks into a
// timestamped directory reached through `..data`. Walking it naively reads every
// document twice, which core then refuses as a binding declared twice in one set —
// so the host would refuse everything delivery gave it, on every pass.
func TestSolutionHostBindingMountSkipsTheAtomicWriterShadowDirectories(t *testing.T) {
	root := t.TempDir()
	timestamped := filepath.Join(root, "..2026_09_27_12_00_00.123456789")
	if err := os.MkdirAll(timestamped, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	name := "acme.prod.crm" + SolutionHostBindingExtension
	write(t, filepath.Join(timestamped, name), "binding: acme.prod.crm\n")
	if err := os.Symlink(timestamped, filepath.Join(root, "..data")); err != nil {
		t.Fatalf("symlink ..data: %v", err)
	}
	if err := os.Symlink(filepath.Join("..data", name), filepath.Join(root, name)); err != nil {
		t.Fatalf("symlink document: %v", err)
	}

	documents, err := NewSolutionHostBindingMount(root).Documents(context.Background())
	if err != nil {
		t.Fatalf("read mount: %v", err)
	}
	if len(documents) != 1 {
		t.Fatalf("read %d documents, want 1: %v", len(documents), sources(documents))
	}
	if string(documents[0].Data) != "binding: acme.prod.crm\n" {
		t.Fatalf("data = %q, want the document the symlink resolves to", documents[0].Data)
	}
}

// A directory that is declared but has not been populated is an empty set: nothing
// has been delivered yet, and that is not an error.
func TestSolutionHostBindingMountAbsentDirectoryIsEmpty(t *testing.T) {
	documents, err := NewSolutionHostBindingMount(filepath.Join(t.TempDir(), "never-mounted")).
		Documents(context.Background())
	if err != nil {
		t.Fatalf("absent mount: %v", err)
	}
	if len(documents) != 0 {
		t.Fatalf("read %d documents from an absent mount", len(documents))
	}
}

// An unreadable mount is an error, never an empty set. Removal is a tombstone
// generation precisely so that a volume that failed to mount cannot be reconciled
// as "withdraw every solution", and that guarantee starts here.
func TestSolutionHostBindingMountUnreadableDirectoryIsAnError(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, filepath.Join(locked, "acme.prod.crm"+SolutionHostBindingExtension), "binding: acme.prod.crm\n")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("cannot make a directory unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores the directory mode")
	}

	if _, err := NewSolutionHostBindingMount(root).Documents(context.Background()); err == nil {
		t.Fatal("an unreadable mount must be reported, not read as an empty desired set")
	}
}

// A document far over the size a binding can legitimately reach is refused as a
// pass-level error rather than parsed.
func TestSolutionHostBindingMountRefusesAnOversizedDocument(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "acme.prod.crm"+SolutionHostBindingExtension),
		strings.Repeat("x", maxSolutionHostBindingBytes+1))

	_, err := NewSolutionHostBindingMount(root).Documents(context.Background())
	if err == nil || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("err = %v, want the size refusal", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func sources(documents []business.SolutionHostBindingDocument) []string {
	names := make([]string, 0, len(documents))
	for _, document := range documents {
		names = append(names, document.Source)
	}
	return names
}
