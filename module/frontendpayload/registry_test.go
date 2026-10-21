package frontendpayload

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryAdapterReadsOnlyTheSelectedCompleteBlob(t *testing.T) {
	data := archiveFor(t, "registry-version")
	frontend := frontendFor(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v2/frontend/blobs/"+string(frontend.Payload.Digest), r.URL.Path)
		w.Header().Set("Docker-Content-Digest", string(frontend.Payload.Digest))
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	source, err := NewRegistrySource(strings.TrimPrefix(server.URL, "http://")+"/frontend", "local", "", "")
	require.NoError(t, err)
	bundle, err := Prepare(t.Context(), source, frontend)
	require.NoError(t, err)
	require.Equal(t, "registry-version", readAsset(t, bundle, "deferred.js"))
	data[bytesIndex(t, data, "registry-version")] = 'X'
	_, err = Prepare(t.Context(), source, frontend)
	require.Error(t, err)
}

func bytesIndex(t *testing.T, data []byte, text string) int {
	t.Helper()
	index := strings.Index(string(data), text)
	require.GreaterOrEqual(t, index, 0)
	return index
}

func TestRegistryRefusesRedirectsAndForeignCredentialRealms(t *testing.T) {
	for _, challenge := range []bool{false, true} {
		foreignCalls := 0
		foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++; w.WriteHeader(500) }))
		defer foreign.Close()
		sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if challenge {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+foreign.URL+`/token",service="registry",scope="repository:frontend:pull"`)
				w.WriteHeader(401)
				return
			}
			http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
		}))
		source, err := NewRegistrySource(strings.TrimPrefix(sourceServer.URL, "http://")+"/frontend", "local", "reader", "secret")
		require.NoError(t, err)
		_, err = Prepare(t.Context(), source, frontendFor(archiveFor(t, "x")))
		require.ErrorIs(t, err, ErrUnavailable)
		require.Zero(t, foreignCalls)
		sourceServer.Close()
	}
}

func TestNativeCacheIsReadOnlyAndPodRefused(t *testing.T) {
	dir := t.TempDir()
	data := archiveFor(t, "native-version")
	frontend := frontendFor(data)
	name := strings.TrimPrefix(string(frontend.Payload.Digest), "sha256:")
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0600))
	_, err := NewDirectorySource(dir, true)
	require.ErrorContains(t, err, "refused in a pod")
	source, err := NewDirectorySource(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	bundle, err := Prepare(t.Context(), source, frontend)
	require.NoError(t, err)
	require.Equal(t, "native-version", readAsset(t, bundle, "deferred.js"))
	require.NoError(t, os.Remove(filepath.Join(dir, name)))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(dir, name)))
	_, err = Prepare(t.Context(), source, frontend)
	require.ErrorIs(t, err, ErrUnavailable)
	for _, config := range [][2]string{{"example.test/frontend", "local"}, {"example.test/frontend", "mesh"}, {"example.test/frontend:latest", "https"}, {"example.test/frontend", ""}} {
		_, err := NewRegistrySource(config[0], config[1], "", "")
		require.Error(t, err)
	}
}
