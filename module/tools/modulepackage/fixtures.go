package modulepackage

import (
	"fmt"
	"os"
	"path/filepath"
)

// SyncEmbeddedFixtures mechanically packages the canonical YAML bytes. It does
// not parse identities, select scenario principals or assemble workspace grants.
func SyncEmbeddedFixtures(moduleRoot string) error {
	source := filepath.Join(moduleRoot, "fixtures")
	destination := filepath.Join(moduleRoot, "services/accounts/code/fixtures/embedded")
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	contents := make(map[string][]byte)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("canonical fixture %q is not a regular file", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		contents[entry.Name()] = data
	}
	if len(contents) == 0 {
		return fmt.Errorf("no canonical YAML fixtures in %s", source)
	}
	if info, err := os.Lstat(destination); err == nil && !info.IsDir() {
		return fmt.Errorf("embedded fixture destination is not a directory: %s", destination)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	existing, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range existing {
		if filepath.Ext(entry.Name()) == ".yaml" && !entry.Type().IsRegular() {
			return fmt.Errorf("embedded fixture %q is not a regular file", entry.Name())
		}
	}
	// ReadDir is sorted: both writes and stale-copy removals have stable order.
	for _, entry := range entries {
		data, ok := contents[entry.Name()]
		if !ok {
			continue
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), data, 0o644); err != nil {
			return err
		}
	}
	for _, entry := range existing {
		if _, present := contents[entry.Name()]; !present && filepath.Ext(entry.Name()) == ".yaml" {
			if err := os.Remove(filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
