package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// R1019-N12. The frontend and accounts each canonicalize a public origin, in different
// languages, and the comparison between them only holds if both settle every spelling
// the same way. Each side asserts BEHAVIOUR on a table of vectors; this holds the two
// tables identical.
//
// Without it the failure is silent in the worst direction: a case fixed on one side
// and left failing on the other reads as two green suites, and the disagreement
// surfaces as a deployed origin refusing itself.
//
// This gate proves only that the two sides assert the same INPUTS and EXPECTATIONS. It
// cannot prove either side's implementation is right — that is what the behavioural
// tests on each side are for, and neither is redundant.
func TestPublicOriginVectorsAgreeAcrossServices(t *testing.T) {
	moduleDir := findModuleDir(t)
	goSource := filepath.Join(moduleDir, "services", "accounts", "code",
		"pkg", "auth", "public_origin_vectors_test.go")
	tsSource := filepath.Join(moduleDir, "services", "frontend", "code",
		"src", "test", "public-origin-canonicalization.test.ts")

	goCanonical := stringLiteralsInBlock(t, goSource, `canonicalPublicOriginVectors = \[\]struct\{ Input, Canonical string \}\{`, "}")
	tsCanonical := stringLiteralsInBlock(t, tsSource, `canonicalPublicOriginVectors: \[string, string\]\[\] = \[`, "]")
	if strings.Join(goCanonical, "|") != strings.Join(tsCanonical, "|") {
		t.Errorf("the canonicalization vectors differ between the two services.\n"+
			"accounts (%s):\n  %v\nfrontend (%s):\n  %v\n"+
			"Both sides must assert the same input/expected pairs, in the same order, or a case "+
			"fixed on one side can be left failing on the other with both suites green.",
			goSource, goCanonical, tsSource, tsCanonical)
	}
	if len(goCanonical) < 20 || len(goCanonical)%2 != 0 {
		t.Fatalf("expected an even, non-trivial number of canonicalization literals, got %d", len(goCanonical))
	}

	goRefused := stringLiteralsInBlock(t, goSource, `refusedPublicOriginVectors = \[\]string\{`, "}")
	tsRefused := stringLiteralsInBlock(t, tsSource, `refusedPublicOriginVectors: string\[\] = \[`, "]")
	if strings.Join(goRefused, "|") != strings.Join(tsRefused, "|") {
		t.Errorf("the refused-origin vectors differ between the two services.\n"+
			"accounts: %v\nfrontend: %v", goRefused, tsRefused)
	}
	if len(goRefused) < 8 {
		t.Fatalf("expected a non-trivial refusal list, got %d entries", len(goRefused))
	}
}

// stringLiteralsInBlock returns every double-quoted literal between the line matching
// opening and the first line whose trimmed text is exactly closing. Both languages
// spell a string literal the same way, which is what makes one extractor enough.
func stringLiteralsInBlock(t *testing.T, path, opening, closing string) []string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	open := regexp.MustCompile(opening)
	literal := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

	var found []string
	inBlock := false
	for _, line := range strings.Split(string(source), "\n") {
		if !inBlock {
			if open.MatchString(line) {
				inBlock = true
			}
			continue
		}
		if strings.TrimSuffix(strings.TrimSpace(line), ";") == closing {
			return found
		}
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		for _, match := range literal.FindAllStringSubmatch(line, -1) {
			found = append(found, match[1])
		}
	}
	if !inBlock {
		t.Fatalf("%s declares no block matching %q — the vectors moved or were renamed, "+
			"which silently retires this gate", path, opening)
	}
	t.Fatalf("%s has no %q closing the block matching %q", path, closing, opening)
	return nil
}
