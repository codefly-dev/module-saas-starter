package frontendpayload

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// metadataServer answers token requests the way the GKE metadata server does,
// counting them; status replaces the answer when it is not 200.
func metadataServer(t *testing.T, status *int, calls *int) *workloadToken {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		require.Equal(t, "Google", r.Header.Get("Metadata-Flavor"))
		require.Equal(t, "/computeMetadata/v1/instance/service-accounts/default/token", r.URL.Path)
		w.WriteHeader(*status)
		_, _ = fmt.Fprintf(w, `{"access_token":"workload-token-%d","expires_in":3600}`, *calls)
	}))
	t.Cleanup(server.Close)
	return &workloadToken{metadataBase: server.URL + "/computeMetadata/v1/", client: server.Client(), now: time.Now}
}

// artifactRegistry behaves as Artifact Registry does for a blob read: a Bearer
// challenge whose realm is on its own host, a token issued only for Basic
// oauth2accesstoken:<workload token>, and the blob answered by a redirect to a
// download path on the same host.
func artifactRegistry(t *testing.T, data []byte, digest string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/token":
			if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("oauth2accesstoken:workload-token-1")) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"token":"registry-token"}`))
		case r.Header.Get("Authorization") != "Bearer registry-token":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/v2/token",service="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v2/project/registry/frontend-bundles/blobs/"+digest:
			http.Redirect(w, r, "/artifacts-downloads/namespaces/project/downloads/opaque", http.StatusFound)
		case r.URL.Path == "/artifacts-downloads/namespaces/project/downloads/opaque":
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// A Pod reads a published bundle end to end as its workload identity: the
// metadata token is exchanged at the registry's own realm, the same-host
// download redirect is followed, and the verified bundle is prepared.
func TestWorkloadSourceReadsAPublishedBundleThroughTheRegistrysOwnRedirect(t *testing.T) {
	data := archiveFor(t, "published-version")
	frontend := frontendFor(data)
	registry := artifactRegistry(t, data, string(frontend.Payload.Digest))
	status, calls := http.StatusOK, 0
	repository := strings.TrimPrefix(registry.URL, "https://") + "/project/registry/frontend-bundles"
	source, err := workloadRegistrySource(repository, metadataServer(t, &status, &calls), registry.Client().Transport)
	require.NoError(t, err)
	bundle, err := Prepare(t.Context(), source, frontend)
	require.NoError(t, err)
	require.Equal(t, "published-version", readAsset(t, bundle, "deferred.js"))

	missing := frontendFor(archiveFor(t, "never-published"))
	_, err = Prepare(t.Context(), source, missing)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorContains(t, err, "holds no blob")
	require.NotContains(t, err.Error(), "token")

	stalled := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artifacts-downloads/capability" {
			http.Redirect(w, r, "/artifacts-downloads/capability", http.StatusFound)
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(stalled.Close)
	slow, err := newRegistrySource(strings.TrimPrefix(stalled.URL, "https://")+"/project/registry/frontend-bundles", "https", func(string) auth.CredentialFunc {
		return auth.StaticCredential("", auth.EmptyCredential)
	}, stalled.Client().Transport)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, err = Prepare(ctx, slow, frontend)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotContains(t, err.Error(), "capability", "a timeout never carries the request URL")
	require.NotContains(t, err.Error(), strings.TrimPrefix(stalled.URL, "https://"))

	status = http.StatusNotFound
	refused, err := workloadRegistrySource(repository, metadataServer(t, &status, new(int)), registry.Client().Transport)
	require.NoError(t, err)
	_, err = Prepare(t.Context(), refused, frontend)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorContains(t, err, "no bound identity")
}

// The reader's credential is held until a minute before it expires, then
// fetched again, and offered to the configured registry alone.
func TestWorkloadCredentialIsTheMetadataTokenForTheRegistryAlone(t *testing.T) {
	status, calls := http.StatusOK, 0
	token := metadataServer(t, &status, &calls)
	clock := time.Now()
	token.now = func() time.Time { return clock }
	source, err := workloadRegistrySource("us-east1-docker.pkg.dev/project/registry/frontend-bundles", token, nil)
	require.NoError(t, err)
	credential := source.repository.Client.(*auth.Client).Credential
	for range 2 {
		got, err := credential(t.Context(), "us-east1-docker.pkg.dev")
		require.NoError(t, err)
		require.Equal(t, auth.Credential{Username: "oauth2accesstoken", Password: "workload-token-1"}, got)
	}
	require.Equal(t, 1, calls)
	clock = clock.Add(59 * time.Minute)
	got, err := credential(t.Context(), "us-east1-docker.pkg.dev")
	require.NoError(t, err)
	require.Equal(t, "workload-token-2", got.Password, "a token within a minute of expiry is fetched again")
	foreign, err := credential(t.Context(), "elsewhere.example")
	require.NoError(t, err)
	require.Equal(t, auth.EmptyCredential, foreign)
	_, err = NewWorkloadRegistrySource("registry.example/frontend-bundles")
	require.ErrorContains(t, err, "workload identity")
}
