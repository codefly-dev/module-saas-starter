package generatedgo

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/tools/imports"
)

// FormatRoots applies the repository's Go formatting contract to every Go file
// below roots. Missing roots are accepted because a contribution kind may have
// no generated Go output in a particular composition.
func FormatRoots(roots ...string) error {
	for _, root := range roots {
		if err := formatRoot(root); err != nil {
			return err
		}
	}
	return nil
}

func formatRoot(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		return formatFile(path)
	})
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("format generated Go root %q: %w", root, err)
	}
	return nil
}

func formatFile(path string) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %q: %w", path, err)
	}
	formatted, err := imports.Process(path, source, nil)
	if err != nil {
		return fmt.Errorf("format %q: %w", path, err)
	}
	if bytes.Equal(source, formatted) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	}
	if err := os.WriteFile(path, formatted, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return nil
}
