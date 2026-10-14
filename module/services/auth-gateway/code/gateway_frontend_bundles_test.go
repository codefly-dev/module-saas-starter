package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/assert"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"
	"github.com/codefly-dev/core/composition"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type bundleSource map[solutionhost.PayloadDigest][]byte

func (s bundleSource) Open(_ context.Context, digest solutionhost.PayloadDigest) (io.ReadCloser, error) {
	data, ok := s[digest]
	if !ok {
		return nil, errors.New("unavailable")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func bundleFixture(t *testing.T, version string) (*solutionhost.Frontend, []byte) {
	t.Helper()
	dir := t.TempDir()
	for name, value := range map[string]string{
		"mf-manifest.json": `{"metaData":{"globalName":"example","publicPath":"auto","remoteEntry":{"name":"entry.js","type":"global"}},"exposes":[{"path":"./Page","assets":{"js":{"async":["lazy.js"]}}}]}`,
		"entry.js":         "import('./lazy.js')", "lazy.js": version,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(value), 0600))
	}
	data, digest, err := composition.CanonicalArchive(dir)
	require.NoError(t, err)
	return &solutionhost.Frontend{Title: "Example", ExposedModule: "./Page", HostContract: 1, Payload: &solutionhost.FrontendPayload{Artifact: "frontend", Digest: solutionhost.PayloadDigest(digest), MediaType: "application/x-tar"}}, data
}

func TestFrontendVersionsRecoverAcrossReplicasAndWithdrawal(t *testing.T) {
	for _, kind := range []accountsv1.SolutionDeclaredKind{accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE, accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION} {
		t.Run(kind.String(), func(t *testing.T) {
			gw, _, _, _ := newGatewayHarness(t)
			upstream := registerSolutionUpstream(t, gw, "example")
			registry := solutionRegistryFake(t, gw)
			record := registry.records["example"]
			record.Declared.Kind = kind
			a, rawA := bundleFixture(t, "A")
			b, rawB := bundleFixture(t, "B")
			source := bundleSource{a.Payload.Digest: rawA, b.Payload.Digest: rawB}
			gw.solutions.frontendSource = source
			set := func(frontend *solutionhost.Frontend, generation uint64) string {
				record.Declared.Generation = generation
				raw, err := json.Marshal(frontend)
				require.NoError(t, err)
				record.Frontend.Manifest = string(raw)
				require.NoError(t, gw.solutions.refresh(t.Context()))
				return frontendAssetVersion(record)
			}
			versionA := set(a, 1)
			raw, err := json.Marshal(a)
			require.NoError(t, err)
			registry.retained = map[string]*accountsv1.SolutionFrontendGeneration{versionA: {TargetId: record.Declared.TargetId, BindingId: record.Declared.BindingId, Generation: 1, Release: record.Declared.Release, Frontend: string(raw), AssetVersion: versionA}}

			versionB := set(b, 2)
			request := func(g *Gateway, version string, want int, body string) {
				r := httptest.NewRequest(http.MethodGet, "/solutions/example/assets/lazy.js", nil)
				r.Header.Set(frontendVersionHeader, version)
				w := httptest.NewRecorder()
				g.ServeHTTP(w, r)
				require.Equal(t, want, w.Code, w.Body.String())
				if want == http.StatusOK {
					require.Equal(t, body, w.Body.String())
					require.Contains(t, w.Header().Get("Cache-Control"), "immutable")
				}
			}
			request(gw, versionA, 200, "A")
			request(gw, versionB, 200, "B")
			cold, _, _, _ := newGatewayHarness(t)
			cold.solutions = newSolutionRegistryCache(registry)
			cold.solutions.frontendSource = source
			require.NoError(t, cold.solutions.refresh(t.Context()))
			request(cold, versionA, 200, "A")
			request(cold, versionB, 200, "B")
			versionRollback := set(a, 3)
			require.NotEqual(t, versionA, versionRollback)
			request(gw, versionRollback, 200, "A")
			delete(source, a.Payload.Digest)
			request(cold, versionA, 200, "A") // verified residency survives source loss
			empty, _, _, _ := newGatewayHarness(t)
			empty.solutions = newSolutionRegistryCache(registry)
			empty.solutions.frontendSource = source
			require.NoError(t, empty.solutions.refresh(t.Context()))
			request(empty, versionA, 503, "")
			request(gw, versionRollback, 200, "A") // current private snapshot survives source loss
			record.Frontend = nil                  // retained A remains usable after a headless update
			record.Declared.Generation = 4
			require.NoError(t, gw.solutions.refresh(t.Context()))
			request(gw, versionA, 200, "A")
			request(gw, frontendAssetVersion(record), 410, "")
			require.Nil(t, upstream.lastHeaders, "immutable requests never reach a mutable upstream")
			record.TombstonedAt = timestamppb.New(time.Now())
			require.NoError(t, gw.solutions.refresh(t.Context()))
			request(gw, versionA, 502, "")
		})
	}
}

type observedBundleSource struct {
	base   bundleSource
	before func()
	calls  atomic.Int32
}

func (s *observedBundleSource) Open(ctx context.Context, digest solutionhost.PayloadDigest) (io.ReadCloser, error) {
	s.calls.Add(1)
	if s.before != nil {
		s.before()
	}
	return s.base.Open(ctx, digest)
}

func TestFrontendPreparationCannotRenewAnExpiredRegistryRead(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")
	registry := solutionRegistryFake(t, gw)
	a, raw := bundleFixture(t, "A")
	encoded, err := json.Marshal(a)
	require.NoError(t, err)
	registry.records["example"].Frontend.Manifest = string(encoded)
	now := time.Now()
	gw.solutions.now = func() time.Time { return now }
	require.NoError(t, gw.solutions.refresh(t.Context()))
	gw.solutions.frontendSource = &observedBundleSource{base: bundleSource{a.Payload.Digest: raw}, before: func() {
		now = now.Add(2 * solutionRegistryLease)
		registry.tombstone("example")
	}}
	require.ErrorIs(t, gw.solutions.refresh(t.Context()), context.DeadlineExceeded)
	require.True(t, gw.solutions.lookupSnapshot("example").stale)
	require.NoError(t, gw.solutions.refresh(t.Context()))
	require.NotNil(t, gw.solutions.lookupSnapshot("example").record.GetTombstonedAt())
}

func TestFrontendConcurrentMissingFilesSharePreparation(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")
	registry := solutionRegistryFake(t, gw)
	record := registry.records["example"]
	a, raw := bundleFixture(t, "A")
	encoded, err := json.Marshal(a)
	require.NoError(t, err)
	version := frontendAssetVersion(record)
	registry.retained = map[string]*accountsv1.SolutionFrontendGeneration{version: {BindingId: record.Declared.BindingId, TargetId: record.Declared.TargetId, Generation: 1, Release: record.Declared.Release, Frontend: string(encoded), AssetVersion: version}}
	record.Declared.Generation = 2
	record.Frontend = nil
	source := &observedBundleSource{base: bundleSource{a.Payload.Digest: raw}}
	gw.solutions.frontendSource = source
	require.NoError(t, gw.solutions.refresh(t.Context()))
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			r := httptest.NewRequest(http.MethodGet, "/solutions/example/assets/missing.js", nil)
			r.Header.Set(frontendVersionHeader, version)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, r)
			assert.Equal(t, 404, w.Code, w.Body.String())
		})
	}
	group.Wait()
	require.Equal(t, int32(1), source.calls.Load())
	registry.declareTarget("example", "replacement", "new-target")
	require.NoError(t, gw.solutions.refresh(t.Context()))
	r := httptest.NewRequest(http.MethodGet, "/solutions/example/assets/lazy.js", nil)
	r.Header.Set(frontendVersionHeader, version)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, r)
	require.Equal(t, 410, w.Code, w.Body.String())
}
