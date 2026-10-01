package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Every committed dogfood example instructs the operator to copy it to the
// matching resolved file; that copy carries operator-owned content and must
// never be committed back. Guard the invariant across all groups so a new
// group cannot ship an example without also ignoring its copy target.
func TestDogfoodExampleCopyTargetsAreGitIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository root")
	}
	root := filepath.Dir(file)
	examples, err := filepath.Glob(
		filepath.Join(root, "configurations", "local-dogfood", "*.env.example"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) == 0 {
		t.Fatal("found no dogfood *.env.example files to check")
	}
	for _, example := range examples {
		target := strings.TrimSuffix(example, ".example")
		relative, err := filepath.Rel(root, target)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(target), func(t *testing.T) {
			command := exec.Command("git", "check-ignore", "-q", relative)
			command.Dir = root
			if err := command.Run(); err != nil {
				t.Fatalf("%s is not git-ignored; a copied example must never be committed back", relative)
			}
		})
	}
}
