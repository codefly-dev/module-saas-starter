package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A configuration key is declared ONCE per file.
//
// The CLI's configuration loader refuses a profile that declares one key under
// two spellings — "configuration definitions conflict" — and it refuses the whole
// profile, not the one key. So a duplicate does not degrade anything: it takes
// down every database-backed suite, the local graph and the deployed boot, all at
// once, and it surfaces as a service that cannot initialize rather than as
// anything naming the file.
//
// This happened here. A merge kept two `APP_BASE_URL=` declarations in
// `application.env`, one from each side, and nothing in this repository noticed
// until five suites failed to boot at the same time. A duplicate is exactly the
// shape a merge produces — both sides appending a documented block for the same
// key — so it is worth a gate that names the file and the key.
func TestConfigurationKeysAreDeclaredOnce(t *testing.T) {
	moduleDir := findModuleDir(t)
	root := filepath.Join(moduleDir, "configurations")

	var checked int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".env") {
			return err
		}
		checked++
		seen := map[string]int{}
		for _, line := range strings.Split(readFileOrFail(t, path), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			key, _, found := strings.Cut(trimmed, "=")
			if !found {
				continue
			}
			seen[strings.TrimSpace(key)]++
		}
		for key, count := range seen {
			if count > 1 {
				rel, relErr := filepath.Rel(moduleDir, path)
				if relErr != nil {
					rel = path
				}
				t.Errorf("%s declares %s %d times. The configuration loader refuses a "+
					"profile with one key under two spellings, and it refuses the WHOLE "+
					"profile — so this takes down every database-backed suite and the "+
					"local run. Declare it once and merge the prose.", rel, key, count)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if checked == 0 {
		t.Fatalf("no .env files found under %s, so this gate asserted nothing", root)
	}
}
