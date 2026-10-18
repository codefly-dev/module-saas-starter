package frontendpayload

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/codefly-dev/core/solutionhost"
)

// DirectorySource reads the CLI's existing native artifact cache. It creates
// and writes nothing. The host refuses this adapter in a deployed environment;
// a local filesystem is not a durable source shared by serving replicas.
type DirectorySource struct{ root *os.Root }

func NewDirectorySource(directory string, inCluster bool) (*DirectorySource, error) {
	if inCluster {
		return nil, errors.New("native frontend artifact cache is refused in a pod")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &DirectorySource{root: root}, nil
}

func (s *DirectorySource) Close() error { return s.root.Close() }

func (s *DirectorySource) Open(ctx context.Context, digest solutionhost.PayloadDigest) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(string(digest), "sha256:")
	if len(name) != 64 || strings.Trim(name, "0123456789abcdef") != "" || !strings.HasPrefix(string(digest), "sha256:") {
		return nil, errors.New("invalid frontend artifact digest")
	}
	info, err := s.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBundleBytes {
		return nil, ErrUnavailable
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, ErrUnavailable
	}
	return file, nil
}
