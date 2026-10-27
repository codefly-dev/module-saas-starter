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

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry"
)

type azureTestCredential struct{ calls int }

func (c *azureTestCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.calls++
	return azcore.AccessToken{Token: "test-workload-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func TestAzureWorkloadSourceExchangesIdentityAndReadsAdmittedBundle(t *testing.T) {
	data := archiveFor(t, "azure-bundle")
	frontend := frontendFor(data)
	digest := string(frontend.Payload.Digest)
	refresh := "e30." + base64.RawStdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))) + ".sig"
	mode := "ok"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/exchange":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "test-workload-token", r.Form.Get("access_token"))
			require.Equal(t, "access_token", r.Form.Get("grant_type"))
			fmt.Fprintf(w, `{"refresh_token":%q}`, refresh)
		case "/oauth2/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, refresh, r.Form.Get("refresh_token"))
			require.Equal(t, "repository:frontend-bundles:pull", r.Form.Get("scope"))
			fmt.Fprint(w, `{"access_token":"test-registry-token"}`)
		default:
			require.Equal(t, "/v2/frontend-bundles/blobs/"+digest, r.URL.Path)
			if r.Header.Get("Authorization") != "Bearer test-registry-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/oauth2/token",service="`+strings.TrimPrefix(server.URL, "https://")+`",scope="repository:frontend-bundles:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if mode == "forbidden" {
				http.Error(w, "private-response-must-not-leak", http.StatusForbidden)
				return
			}
			if mode == "wrong-digest" {
				w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("0", 64))
			} else {
				w.Header().Set("Docker-Content-Digest", digest)
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
		}
	}))
	defer server.Close()
	ref, err := registry.ParseReference(strings.TrimPrefix(server.URL, "https://") + "/frontend-bundles")
	require.NoError(t, err)
	credential := &azureTestCredential{}
	source, err := azureRegistrySource(ref, credential, server.Client().Transport)
	require.NoError(t, err)
	bundle, err := Prepare(t.Context(), source, frontend)
	require.NoError(t, err)
	require.Equal(t, "azure-bundle", readAsset(t, bundle, "deferred.js"))
	require.Equal(t, 1, credential.calls)
	mode = "wrong-digest"
	_, err = source.Open(t.Context(), frontend.Payload.Digest)
	require.ErrorContains(t, err, "descriptor differs")
	mode = "forbidden"
	_, err = source.Open(t.Context(), frontend.Payload.Digest)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorContains(t, err, "HTTP 403")
	require.NotContains(t, err.Error(), "private-response")
	require.Equal(t, 1, credential.calls, "the SDK reuses its workload token exchange")
}
