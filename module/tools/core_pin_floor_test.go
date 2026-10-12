package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The core pin must not be "upgraded" into v0.8.x, which is NEWER and contains
// LESS.
//
// Core's main branch cut `v0.8.0` and `v0.8.1` from its own release commits
// while the work this module depends on sat unmerged on a branch. Those tags
// therefore carry **no `workcontext` package and no `solutionhost` static half**
// — the two packages the declared-presence and Work Context work is built on.
//
// Semver says v0.8.1 is newer than the v0.7.2 pseudo-version pinned here, so
// every bulk upgrade path points at it: `go get -u`, `go get core@latest`, and
// this toolchain's own `codefly update workspace` / `update deps`, which are
// already known to move pins past documented ceilings. A reviewer reading
// "latest release is v0.8.1, why is this on a v0.7.2 pseudo-version?" will
// reasonably try to fix it.
//
// The failure that follows is loud but badly misleading: the build reports that
// the module does not contain `solutionhost`, which reads as a broken pin rather
// than a deliberate one. The real target is **v0.9.0**, cut from the merge that
// carries those packages.
//
// This gate is the only thing that holds, because a comment in a go.mod does not
// survive a bulk update verb.
func TestCorePinIsNotAVersionThatDroppedTheWorkThisModuleNeeds(t *testing.T) {
	moduleDir := findModuleDir(t)

	// Every Go module in the tree that pins core. Discovered rather than listed:
	// a new service pinning core must be covered without anyone remembering to
	// add it here, which is how the policy-log service arrived.
	var found []string
	err := filepath.Walk(moduleDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// node_modules holds no go.mod worth reading and is enormous.
			if info.Name() == "node_modules" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() != "go.mod" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		pin := regexp.MustCompile(`(?m)^\s*github\.com/codefly-dev/core\s+(\S+)`).FindStringSubmatch(string(data))
		if pin == nil {
			return nil
		}
		relative, _ := filepath.Rel(moduleDir, path)
		found = append(found, relative+" "+pin[1])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", moduleDir, err)
	}
	if len(found) == 0 {
		t.Fatal("no go.mod pins github.com/codefly-dev/core, so this gate measures nothing — " +
			"the pin it was written for is in services/accounts/code/go.mod")
	}
	sort.Strings(found)

	for _, entry := range found {
		parts := strings.Fields(entry)
		file, version := parts[0], parts[1]
		// v0.8.x only. Not a general floor: pinning an OLDER core is a different
		// mistake with a different symptom, and a gate that refused every
		// version but one would fight the next legitimate bump.
		if strings.HasPrefix(version, "v0.8.") {
			t.Errorf("%s pins core %s, which is newer than this module's pin and contains LESS: "+
				"v0.8.0 and v0.8.1 were cut from core's main while the workcontext package and the solutionhost "+
				"static half sat unmerged, so neither tag carries them. The build will report that the module does "+
				"not contain solutionhost, which reads as a broken pin rather than a deliberate one. The target is "+
				"v0.9.0, cut from the merge that carries those packages.", file, version)
		}
	}
}
