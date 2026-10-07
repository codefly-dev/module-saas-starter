package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every SOLUTION_HOST_* setting the code reads is documented, and every one the
// documentation names is read.
//
// WHY THIS EXISTS. `SOLUTION_HOST_SIGNER_DOMAINS` was deleted from the code when
// the verifier took over the signer-to-domain mapping, and
// `SOLUTION_REGISTRATION.md` kept a table row describing it as required. In the
// same table, `SOLUTION_HOST_TRUST_POLICY` still offered `local` after the code
// had stopped accepting it.
//
// A configuration table is the ONE place a reader goes to find out what a value
// may be, so a deleted setting left in it is not a stale sentence — it is an
// instruction to configure a host that refuses to boot. The other direction is
// just as bad: a setting the code reads and nothing documents is one an operator
// discovers from a boot failure.
//
// Prose can be wrong in a hundred ways and no test can catch them. The NAMES are
// mechanical, so this catches the half that is.
func TestSolutionHostSettingsAreDocumentedAndRead(t *testing.T) {
	moduleDir := findModuleDir(t)

	code := readFileOrFail(t, filepath.Join(moduleDir, "services", "accounts", "code", "work.go"))
	document := readFileOrFail(t, filepath.Join(moduleDir, "SOLUTION_REGISTRATION.md"))

	// Read from the code via workspaceEnv, which is the only way a setting
	// reaches this service — a name in a comment is not a setting, so the match
	// is on the call rather than on the bare name.
	readPattern := regexp.MustCompile(`workspaceEnv\([^)]*"(SOLUTION_HOST_[A-Z_]+)"\)`)
	read := map[string]bool{}
	for _, match := range readPattern.FindAllStringSubmatch(code, -1) {
		read[match[1]] = true
	}
	if len(read) == 0 {
		t.Fatal("found no SOLUTION_HOST_* settings read in work.go — this gate cannot pass by finding nothing")
	}

	// From the document, only names in a table cell. A name in prose may well be
	// discussing a setting that was removed, which is exactly what the fixed
	// text now does for SOLUTION_HOST_SIGNER_DOMAINS, and that must stay legal.
	documented := map[string]bool{}
	cellPattern := regexp.MustCompile("^\\|\\s*`(SOLUTION_HOST_[A-Z_]+)`\\s*\\|")
	for _, line := range strings.Split(document, "\n") {
		if match := cellPattern.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			documented[match[1]] = true
		}
	}

	var undocumented, unread []string
	for name := range read {
		if !documented[name] {
			undocumented = append(undocumented, name)
		}
	}
	for name := range documented {
		if !read[name] {
			unread = append(unread, name)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(unread)

	if len(undocumented) > 0 {
		t.Errorf("work.go reads these settings and SOLUTION_REGISTRATION.md's tables do not list them:\n    %s\n\n"+
			"An operator discovers an undocumented setting from a boot failure. Add a table row.",
			strings.Join(undocumented, "\n    "))
	}
	if len(unread) > 0 {
		t.Errorf("SOLUTION_REGISTRATION.md's tables list these settings and work.go reads none of them:\n    %s\n\n"+
			"A deleted setting left in a configuration table is an instruction to configure a host that refuses "+
			"to boot. Remove the row — discussing the removal in PROSE is fine and does not trip this gate.",
			strings.Join(unread, "\n    "))
	}
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
