// Package frontendpayload prepares complete, immutable frontend bundles for the
// host. Artifact custody belongs to the configured source; these byte snapshots
// are disposable and may always be rebuilt from the signed payload identity.
package frontendpayload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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
	payload solutionhost.FrontendPayload
	files   map[string][]byte
}

func (b *Bundle) Matches(payload *solutionhost.FrontendPayload) bool {
	return b != nil && payload != nil && b.payload == *payload
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
	staging, err := os.MkdirTemp("", "frontend-prepare-")
	if err != nil {
		return nil, fmt.Errorf("prepare frontend scratch directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := composition.ExtractArchive(ctx, archive, staging); err != nil {
		return nil, fmt.Errorf("unsafe frontend bundle: %w", err)
	}
	bundle := &Bundle{payload: payload, files: map[string][]byte{}}
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
	if err := bundle.checkManifest(); err != nil {
		return nil, err
	}
	return bundle, nil
}

func (b *Bundle) checkManifest() error {
	var manifest struct {
		MetaData struct {
			RemoteEntry struct{ Name, Path string }
			PublicPath  string
		}
	}
	if err := json.Unmarshal(b.files["mf-manifest.json"], &manifest); err != nil {
		return errors.New("frontend bundle has no readable federation manifest")
	}
	entry := manifest.MetaData.RemoteEntry
	// Absolute public paths escape the admitted root. The maintained producer
	// uses an automatic/relative public path for entries and deferred chunks.
	if manifest.MetaData.PublicPath != "" && manifest.MetaData.PublicPath != "auto" && manifest.MetaData.PublicPath != "./" {
		return errors.New("frontend bundle public path must be relative")
	}
	if entry.Name == "" || strings.HasPrefix(entry.Path, "/") || strings.Contains(entry.Path, "..") {
		return errors.New("frontend bundle has no relative remote entry")
	}
	_, _, err := b.Open(path.Join(entry.Path, entry.Name))
	if err != nil {
		return errors.New("frontend bundle is missing its remote entry")
	}
	return nil
}
