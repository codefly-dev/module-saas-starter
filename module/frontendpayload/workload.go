package frontendpayload

// LABELLED STOPGAP — this file names a cloud vendor, and a host should not.
//
// A deployed render publishes frontend bundles to the cell's own registry and
// hands the host that repository (artifact-cache/REPOSITORY). The host reads
// it as the workload it is, with no stored credential: on Google Artifact
// Registry the metadata server issues the pod's bound identity a token, which
// the registry accepts as the password of "oauth2accesstoken". Nothing is
// delivered to the pod, written to disk or rotated by hand. The same metadata
// read backs accounts' key service (services/accounts/code/pkg/keyservice),
// which carries the same label for the same reason: Codefly has no
// "this cell's workload identity" dependency kind yet, so there is nothing
// to resolve a reader through. When one exists this moves below that seam.
// Azure Container Registry uses the pod's federated workload identity through
// the Azure SDK (azure.go). Registries without a workload reader are refused.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"oras.land/oras-go/v2/registry/remote/auth"
)

const workloadMetadataBase = "http://metadata.google.internal/computeMetadata/v1/"

// NewWorkloadRegistrySource reads a published bundle repository over HTTPS as
// the pod's own workload identity.
// Unsupported registries are refused when the host starts, by name: failing closed
// at boot is visible, where an anonymous read would leave every page pending.
func NewWorkloadRegistrySource(repository string) (Source, error) {
	host, _, _ := strings.Cut(repository, "/")
	if strings.HasSuffix(host, ".azurecr.io") {
		return newAzureWorkloadSource(repository)
	}
	if !strings.HasSuffix(host, ".pkg.dev") {
		return nil, fmt.Errorf("frontend artifact repository %q is not on a registry this host reads as its workload identity (Artifact Registry or Azure Container Registry)", repository)
	}
	return workloadRegistrySource(repository, &workloadToken{metadataBase: workloadMetadataBase, client: &http.Client{Timeout: 10 * time.Second}, now: time.Now}, nil)
}

func workloadRegistrySource(repository string, token *workloadToken, base http.RoundTripper) (*RegistrySource, error) {
	return newRegistrySource(repository, "https", func(registry string) auth.CredentialFunc {
		return func(ctx context.Context, hostport string) (auth.Credential, error) {
			if hostport != registry {
				return auth.EmptyCredential, nil
			}
			value, err := token.get(ctx)
			if err != nil {
				return auth.EmptyCredential, err
			}
			return auth.Credential{Username: "oauth2accesstoken", Password: value}, nil
		}
	}, base)
}

// readerIdentityError is a refusal of the reader's own identity: its reason
// names the metadata server's answer and never carries a token.
type readerIdentityError struct{ reason string }

func (e *readerIdentityError) Error() string { return e.reason }

// workloadToken holds the metadata server's current token in memory and
// refreshes it a minute before it expires.
type workloadToken struct {
	metadataBase string
	client       *http.Client
	mu           sync.Mutex
	now          func() time.Time
	value        string
	expires      time.Time
}

func (t *workloadToken) get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.value != "" && t.now().Before(t.expires) {
		return t.value, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.metadataBase+"instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Metadata-Flavor", "Google")
	response, err := t.client.Do(request)
	if err != nil {
		return "", &readerIdentityError{"the metadata server issued no workload identity token"}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", &readerIdentityError{fmt.Sprintf("the metadata server refused a workload identity token (HTTP %d); the pod has no bound identity", response.StatusCode)}
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&payload); err != nil || payload.AccessToken == "" {
		return "", &readerIdentityError{"the metadata server returned no access token"}
	}
	lifetime := time.Duration(payload.ExpiresIn) * time.Second
	if lifetime > time.Minute {
		lifetime -= time.Minute
	}
	t.value, t.expires = payload.AccessToken, t.now().Add(lifetime)
	return t.value, nil
}
