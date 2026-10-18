// Package frontendpayload prepares complete, immutable frontend bundles for the
// host. Artifact custody belongs to the configured source; these byte snapshots
// are disposable and may always be rebuilt from the signed payload identity.
package frontendpayload

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/codefly-dev/core/composition"
	"github.com/codefly-dev/core/solutionhost"
)

const MaxBundleBytes = 64 << 20

var ErrUnavailable = errors.New("frontend artifact unavailable")

// Source opens the complete blob named by an admitted digest. Source placement
// and reader identity are platform configuration, never input from a browser or
// a contributing workload. The reader must honor ctx, including cancellation.
type Source interface {
	Open(context.Context, solutionhost.PayloadDigest) (io.ReadCloser, error)
}

// Bundle exposes only reads of a private snapshot. It never serves from the
// source or a filesystem that can change after verification.
type Bundle struct {
	declaration [sha256.Size]byte
	files       map[string][]byte
}

func (b *Bundle) Matches(frontend *solutionhost.Frontend) bool {
	encoded, err := json.Marshal(frontend)
	return b != nil && frontend != nil && err == nil && b.declaration == sha256.Sum256(encoded)
}

// Open returns a fresh reader, so concurrent requests cannot change one
// another's position or mutate the verified bytes.
func (b *Bundle) Open(name string) (io.ReadSeeker, int64, error) {
	if b == nil || !fs.ValidPath(name) || strings.ContainsAny(name, "\\%") {
		return nil, 0, fs.ErrNotExist
	}
	data, ok := b.files[name]
	if !ok {
		return nil, 0, fs.ErrNotExist
	}
	return bytes.NewReader(data), int64(len(data)), nil
}

// Prepare verifies the entire archive before extracting or publishing any file.
// Core owns safe tar extraction. The host owns the supported federation profile.
// Preparation failure leaves the caller's previously applied generation intact.
func Prepare(ctx context.Context, source Source, frontend *solutionhost.Frontend) (*Bundle, error) {
	if source == nil {
		return nil, ErrUnavailable
	}
	if frontend == nil || frontend.Payload == nil || frontend.Payload.MediaType != "application/x-tar" || frontend.HostContract != 1 {
		return nil, errors.New("frontend bundle requires tar payload and host contract 1")
	}
	payload := *frontend.Payload
	reader, err := source.Open(ctx, payload.Digest)
	if err != nil {
		// Transport errors may include credentials or a private source locator.
		return nil, ErrUnavailable
	}
	if reader == nil {
		return nil, ErrUnavailable
	}
	defer reader.Close()
	archive, err := io.ReadAll(io.LimitReader(reader, MaxBundleBytes+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(archive) > MaxBundleBytes {
		return nil, errors.New("frontend bundle exceeds size limit")
	}
	if got := fmt.Sprintf("sha256:%x", sha256.Sum256(archive)); got != string(payload.Digest) {
		return nil, errors.New("frontend bundle digest mismatch")
	}
	// Core owns extraction; this frontend profile has a smaller expansion
	// budget than Core's general archive limit. Check before touching disk.
	if err := checkExpandedSize(ctx, archive); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp("", "frontend-prepare-")
	if err != nil {
		return nil, fmt.Errorf("prepare frontend scratch directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := composition.ExtractArchive(ctx, archive, staging); err != nil {
		return nil, fmt.Errorf("unsafe frontend bundle: %w", err)
	}
	encoded, err := json.Marshal(frontend)
	if err != nil {
		return nil, errors.New("frontend declaration cannot be encoded")
	}
	bundle := &Bundle{declaration: sha256.Sum256(encoded), files: map[string][]byte{}}
	err = filepath.WalkDir(staging, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(staging, filename)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(filename)
		if err == nil {
			bundle.files[filepath.ToSlash(name)] = data
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot frontend bundle: %w", err)
	}
	if err := bundle.checkManifest(frontend); err != nil {
		return nil, err
	}
	return bundle, nil
}

func checkExpandedSize(ctx context.Context, archive []byte) error {
	reader := tar.NewReader(bytes.NewReader(archive))
	var expanded int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read frontend archive: %w", err)
		}
		if header.Size < 0 || header.Size > MaxBundleBytes-expanded {
			return errors.New("frontend bundle expanded size limit exceeded")
		}
		expanded += header.Size
	}
}
