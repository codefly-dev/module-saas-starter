package infra

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"accounts/pkg/business"
)

// The delivered desired set, as files (issue #952).
//
// Delivery places the bytes; this reads them. The host deliberately knows nothing
// about how they got there: codefly-dev/cli renders one document per solution
// instance and Argo syncs it, today as a ConfigMap whose one data key is the
// document, which a projected volume presents to this process as a file. A local
// `codefly run` hands over a plain directory of the same files. Neither shape is
// named here, because a host that read Kubernetes would be a host that cannot be
// run on a laptop.
//
// Two rules make the reader safe rather than merely convenient:
//
// An unreadable mount is an ERROR, never an empty set. Removal is a tombstone
// generation precisely so that a volume that failed to mount, a directory that
// was renamed, or a permission that was tightened can never be reconciled as
// "remove every solution". A missing directory is the one benign case — nothing
// has been delivered yet — and is reported as an empty set.
//
// Kubernetes' atomic writer is honoured. A projected ConfigMap volume is a
// directory of symlinks into a timestamped `..2026_09_27_12_00_00.123456789`
// directory, reached through a `..data` symlink, so the same document is visible
// twice under two names. Every path element beginning with a dot is skipped,
// which is exactly that convention, and is also why a partially written update
// is never read: the writer swaps the `..data` symlink atomically.

// SolutionHostBindingExtension is the suffix a delivered document's file name
// carries. It matches what the renderer names its ConfigMap data key.
const SolutionHostBindingExtension = ".codefly.yaml"

// maxSolutionHostBindingBytes bounds one document. A binding carries a release,
// a route, a handful of artifact digests, module pins and endpoints; 256 KiB is
// far above any of those and matches what the registry accepts for a manifest.
const maxSolutionHostBindingBytes = 256 << 10

// SolutionHostBindingMount reads the delivered documents from a directory.
type SolutionHostBindingMount struct {
	root string
}

// NewSolutionHostBindingMount reads documents from root.
func NewSolutionHostBindingMount(root string) *SolutionHostBindingMount {
	return &SolutionHostBindingMount{root: root}
}

// Documents returns every delivered document under the mount, ordered by path so
// a pass reads the same set in the same order twice.
func (m *SolutionHostBindingMount) Documents(
	ctx context.Context,
) ([]business.SolutionHostBindingDocument, error) {
	if _, err := os.Stat(m.root); err != nil {
		if os.IsNotExist(err) {
			// Nothing delivered yet. This is the only absence that is not an
			// error: the directory a deployment declares but has not populated.
			return nil, nil
		}
		return nil, fmt.Errorf("stat solution host binding mount %s: %w", m.root, err)
	}
	var paths []string
	walkErr := filepath.WalkDir(m.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		// `..data`, `..2026_…` and any other dot-prefixed element: Kubernetes'
		// atomic writer, and the convention for "not content". Skipping the
		// directory rather than the file is what stops one document being read
		// twice under two names.
		if path != m.root && strings.HasPrefix(name, ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(name, SolutionHostBindingExtension) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk solution host binding mount %s: %w", m.root, walkErr)
	}
	sort.Strings(paths)
	documents := make([]business.SolutionHostBindingDocument, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat delivered solution host binding %s: %w", path, err)
		}
		if info.Size() > maxSolutionHostBindingBytes {
			return nil, fmt.Errorf("delivered solution host binding %s is %d bytes, over the %d-byte limit",
				path, info.Size(), maxSolutionHostBindingBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read delivered solution host binding %s: %w", path, err)
		}
		documents = append(documents, business.SolutionHostBindingDocument{Source: path, Data: data})
	}
	return documents, nil
}

var _ business.SolutionHostBindingSource = (*SolutionHostBindingMount)(nil)
