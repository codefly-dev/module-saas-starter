package main

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestRuntimePluginOwnsNoGitOrArgoTransport is the boundary guard for issue #37.
//
// Scope: the module-generation plugin, which is package main at the repository
// root (main.go, gitops.go). It must emit module-owned manifests and
// transport-neutral metadata only, with no Git binary, network, or cluster
// access during generation. Publication, revision selection, and Application /
// AppProject assembly belong to the CLI/server promotion driver, not here.
//
// The sibling host/ package is deliberately out of scope: it is the
// saas-starter policy backend (an HTTP client the codefly CLI wires in at
// runtime), not the generation plugin. It legitimately performs network I/O and
// touches no Git or Argo transport. The scan is intentionally non-recursive so
// that boundary stays explicit.
func TestRuntimePluginOwnsNoGitOrArgoTransport(t *testing.T) {
	forbiddenImports := map[string]string{
		"os/exec":  "executes external binaries such as git",
		"net/url":  "parses repository transport URLs",
		"net/http": "reaches network services during generation",
	}
	forbiddenTokens := []string{
		"argoproj.io",
		"toolkit.fluxcd.io",
		"AppProject",
		"repoURL",
		"targetRevision",
		"sourceRepos",
		"rev-parse",
		"verify-tag",
		"refs/tags",
		"git archive",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range forbiddenTokens {
			if strings.Contains(string(data), token) {
				t.Errorf("%s contains forbidden repository/Argo transport token %q", name, token)
			}
		}
		file, err := parser.ParseFile(fset, name, data, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imported := range file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if reason, forbidden := forbiddenImports[path]; forbidden {
				t.Errorf("%s imports %q, which %s", name, path, reason)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("boundary test scanned no runtime plugin files")
	}
}
