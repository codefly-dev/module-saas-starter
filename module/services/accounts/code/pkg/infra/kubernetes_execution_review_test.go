//go:build pure

package infra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/business/installationtest"
	"accounts/pkg/business/moduleregistrytest"

	"github.com/stretchr/testify/require"
)

// Review round 1 on #1056 minted through the REAL Kubernetes decoder and the
// real BindExecution with a mocked API server: one pod's own token as another
// prefix, a pod with unapproved sidecar and ephemeral containers, a terminated
// app, and a completed init container standing in for the app. These are those
// probes, turned into refusals.

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var reviewDigest = "sha256:" + strings.Repeat("1", 64)

// reviewToken is a JWT-shaped projected token with the given lifetime. Its
// signature is the API server's to judge; the fake API server accepts it.
func reviewToken(t *testing.T, lifetime time.Duration) string {
	t.Helper()
	now := time.Now().Unix()
	claims, err := json.Marshal(map[string]any{"iat": now, "exp": now + int64(lifetime/time.Second), "aud": []string{"accounts"}})
	require.NoError(t, err)
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"RS256"}`)) + "." + encode(claims) + "." + encode([]byte("signature"))
}

// reviewClient is a KubernetesClient whose API server authenticates any token
// as acme-prod/shared on pod alpha (uid pod-alpha), and answers that pod with
// the given spec and status.
func reviewClient(t *testing.T, spec, status string) *KubernetesClient {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host-token")
	require.NoError(t, os.WriteFile(path, []byte("host-test-token"), 0o600))
	pod := `{"metadata":{"uid":"pod-alpha"},"spec":{` + spec + `},"status":{` + status + `}}`
	return &KubernetesClient{endpoint: "https://api.example", tokenPath: path, http: &http.Client{Transport: reviewTransport(
		func(r *http.Request) (*http.Response, error) {
			body := pod
			if strings.HasSuffix(r.URL.Path, "tokenreviews") {
				var request tokenReviewRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					return nil, err
				}
				if len(request.Spec.Audiences) != 1 || request.Spec.Audiences[0] != "accounts" {
					return nil, fmt.Errorf("wrong audience")
				}
				body = `{"status":{"authenticated":true,"audiences":["accounts"],"user":{"username":"system:serviceaccount:acme-prod:shared",` +
					`"extra":{"authentication.kubernetes.io/pod-name":["alpha"],"authentication.kubernetes.io/pod-uid":["pod-alpha"]}}}}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}}
}

func reviewService(t *testing.T, client *KubernetesClient, registry string) *business.Service {
	t.Helper()
	parsed, err := moduleregistrytest.Parse(registry)
	require.NoError(t, err)
	service := &business.Service{}
	installations := &installationtest.Store{}
	for principal, grant := range parsed {
		grant.PresenceBinding = "example." + grant.Prefix
		parsed[principal] = grant
		scope := business.FullInstallationScope(grant)
		_, err := installations.InstallSolution(context.Background(), &business.InstallSolutionParams{OrgID: grant.Tenant, RegistryPrincipalID: principal, RegistryBindingID: grant.PresenceBinding, Scope: &scope})
		require.NoError(t, err)
	}
	service.SetModulePrincipals(parsed)
	service.SetModuleAuthorityReads(installations, nil)
	service.SetExecutionBinding(client)
	return service
}

const (
	reviewAlphaOnly = `{"alpha":{"tenant":"11111111-1111-4111-8111-111111111111","workload":{"namespace":"acme-prod","service_account":"shared","container":"app"}}}`
	reviewShared    = `{"alpha":{"tenant":"11111111-1111-4111-8111-111111111111","workload":{"namespace":"acme-prod","service_account":"shared","container":"app"}},` +
		`"beta":{"tenant":"22222222-2222-4222-8222-222222222222","workload":{"namespace":"acme-prod","service_account":"shared","container":"app"}}}`
	reviewAppSpec   = `"containers":[{"name":"app","image":"example/app"}]`
	reviewAppStatus = `"phase":"Running","containerStatuses":[{"name":"app","imageID":"example/app@` + "sha256:1111111111111111111111111111111111111111111111111111111111111111" + `","state":{"running":{}}}]`
)

// The control, so every refusal below is about its one difference.
func TestReviewedRunningAppAloneMints(t *testing.T) {
	service := reviewService(t, reviewClient(t, reviewAppSpec, reviewAppStatus), reviewAlphaOnly)
	authority, err := service.ModuleAuthorizeWorkContext(business.WithVerifiedWorkloadPresenter(context.Background(), "spiffe://cluster.local/ns/acme-prod/sa/shared"), "alpha", reviewToken(t, 10*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "pod-alpha", authority.Execution.PodUID)
}

// #1145: containerd reports, as a container's status.imageID, the FIRST digest
// it learned for the content, so a build re-published unchanged under a new
// digest reads as the old one. Nothing at mint reads an image any more: a pod
// whose spec and status name different digests for the same container mints,
// because the workload is what binds, never the build.
func TestAnAliasedImageIDIsNotReadAtMint(t *testing.T) {
	spec := `"containers":[{"name":"app","image":"registry.example/app@sha256:` + strings.Repeat("a", 64) + `"}]`
	status := `"phase":"Running","containerStatuses":[{"name":"app","imageID":"registry.example/app@sha256:` + strings.Repeat("b", 64) + `","state":{"running":{}}}]`
	service := reviewService(t, reviewClient(t, spec, status), reviewAlphaOnly)
	authority, err := service.ModuleAuthorizeWorkContext(business.WithVerifiedWorkloadPresenter(context.Background(), "spiffe://cluster.local/ns/acme-prod/sa/shared"), "alpha", reviewToken(t, 10*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "app", authority.Execution.ContainerName)
}

// FINDING 1: alpha's own token minted as beta (and alpha) when the two share a
// workload. Now neither.
func TestReviewSharedWorkloadMintsAsNeitherPrefix(t *testing.T) {
	service := reviewService(t, reviewClient(t, reviewAppSpec, reviewAppStatus), reviewShared)
	for _, prefix := range []string{"alpha", "beta"} {
		_, err := service.ModuleAuthorizeWorkContext(business.WithVerifiedWorkloadPresenter(context.Background(), "spiffe://cluster.local/ns/acme-prod/sa/shared"), prefix, reviewToken(t, 10*time.Minute))
		require.ErrorIs(t, err, business.ErrModuleIdentityDenied, prefix)
		require.ErrorIs(t, err, business.ErrExecutionWorkloadAmbiguous, prefix)
	}
}

// FINDINGS 2a and 3, through the real decoder.
func TestReviewPodsTheApprovalDoesNotCoverMintNothing(t *testing.T) {
	other := "sha256:" + strings.Repeat("2", 64)
	for name, tc := range map[string]struct {
		spec, status string
		want         error
	}{
		"unapproved sidecar and ephemeral containers": {
			`"containers":[{"name":"app","image":"example/app"},{"name":"sidecar","image":"example/sidecar"}],"ephemeralContainers":[{"name":"debug","image":"example/debug"}]`,
			`"phase":"Running","containerStatuses":[{"name":"app","imageID":"` + reviewDigest + `","state":{"running":{}}},{"name":"sidecar","imageID":"` + other + `","state":{"running":{}}}],` +
				`"ephemeralContainerStatuses":[{"name":"debug","imageID":"` + other + `","state":{"running":{}}}]`,
			business.ErrExecutionUnapprovedContainer,
		},
		"an ephemeral container alone, already exited": {
			`"containers":[{"name":"app","image":"example/app"}],"ephemeralContainers":[{"name":"debug","image":"example/debug"}]`,
			`"phase":"Running","containerStatuses":[{"name":"app","imageID":"` + reviewDigest + `","state":{"running":{}}}],` +
				`"ephemeralContainerStatuses":[{"name":"debug","imageID":"` + other + `","state":{"terminated":{"exitCode":0}}}]`,
			business.ErrExecutionUnapprovedContainer,
		},
		"a restartable init container (a native sidecar)": {
			`"containers":[{"name":"app","image":"example/app"}],"initContainers":[{"name":"proxy","image":"example/proxy","restartPolicy":"Always"}]`,
			`"phase":"Running","containerStatuses":[{"name":"app","imageID":"` + reviewDigest + `","state":{"running":{}}}],` +
				`"initContainerStatuses":[{"name":"proxy","imageID":"` + other + `","state":{"running":{}}}]`,
			business.ErrExecutionUnapprovedContainer,
		},
		"a terminated app": {
			reviewAppSpec,
			`"phase":"Running","containerStatuses":[{"name":"app","imageID":"` + reviewDigest + `","state":{"terminated":{"exitCode":0}}}]`,
			business.ErrExecutionNotRunning,
		},
		"a completed init container named like the app": {
			`"containers":[{"name":"sidecar","image":"example/sidecar"}],"initContainers":[{"name":"app","image":"example/app"}]`,
			`"phase":"Running","initContainerStatuses":[{"name":"app","imageID":"` + reviewDigest + `","state":{"terminated":{"exitCode":0}}}]`,
			business.ErrExecutionUnapprovedContainer,
		},
		"a pod being deleted": {
			reviewAppSpec, reviewAppStatus, business.ErrExecutionNotRunning,
		},
		"a pod that has Succeeded": {
			reviewAppSpec,
			`"phase":"Succeeded","containerStatuses":[{"name":"app","imageID":"` + reviewDigest + `","state":{"running":{}}}]`,
			business.ErrExecutionNotRunning,
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := reviewClient(t, tc.spec, tc.status)
			if name == "a pod being deleted" {
				client = reviewClientDeleting(t)
			}
			service := reviewService(t, client, reviewAlphaOnly)
			_, err := service.ModuleAuthorizeWorkContext(business.WithVerifiedWorkloadPresenter(context.Background(), "spiffe://cluster.local/ns/acme-prod/sa/shared"), "alpha", reviewToken(t, 10*time.Minute))
			require.ErrorIs(t, err, tc.want)
			require.ErrorIs(t, err, business.ErrModuleIdentityDenied)
		})
	}
}

func reviewClientDeleting(t *testing.T) *KubernetesClient {
	t.Helper()
	client := reviewClient(t, reviewAppSpec, reviewAppStatus)
	inner := client.http.Transport.(reviewTransport)
	client.http = &http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		response, err := inner(r)
		if err != nil || strings.HasSuffix(r.URL.Path, "tokenreviews") {
			return response, err
		}
		raw, _ := io.ReadAll(response.Body)
		patched := strings.Replace(string(raw), `"metadata":{"uid":"pod-alpha"}`, `"metadata":{"uid":"pod-alpha","deletionTimestamp":"2026-10-11T00:00:00Z"}`, 1)
		response.Body = io.NopCloser(strings.NewReader(patched))
		return response, nil
	})}
	return client
}

// FINDING 2b, through the real decoder: the token's own iat and exp bound it.
func TestReviewATokenLongerLivedThanTenMinutesMintsNothing(t *testing.T) {
	service := reviewService(t, reviewClient(t, reviewAppSpec, reviewAppStatus), reviewAlphaOnly)
	_, err := service.ModuleAuthorizeWorkContext(business.WithVerifiedWorkloadPresenter(context.Background(), "spiffe://cluster.local/ns/acme-prod/sa/shared"), "alpha", reviewToken(t, time.Hour))
	require.ErrorIs(t, err, business.ErrExecutionTokenLifetime)

	// A token the review authenticates but whose claims cannot be read is
	// refused rather than given an assumed lifetime.
	_, err = service.ModuleAuthorizeWorkContext(business.WithVerifiedWorkloadPresenter(context.Background(), "spiffe://cluster.local/ns/acme-prod/sa/shared"), "alpha", "copied-valid-alpha-token")
	require.ErrorIs(t, err, business.ErrExecutionTokenRejected)
}

// Exercise presenter binding through the production Kubernetes decoder and
// business authorization, with a valid copied token and an approved live pod.
func TestProductionPresenterMustMatchAppliedAndReviewedWorkload(t *testing.T) {
	client := reviewClient(t, reviewAppSpec, reviewAppStatus)
	require.False(t, client.NativeDevelopment())
	service := reviewService(t, client, reviewAlphaOnly)
	token := reviewToken(t, 10*time.Minute)
	const valid = "spiffe://cluster.local/ns/acme-prod/sa/shared"
	for _, presenter := range []string{"", "spiffe://cluster.local/ns/other/sa/shared", "spiffe://cluster.local/ns/acme-prod/sa/attacker", valid + "?"} {
		t.Run(presenter, func(t *testing.T) {
			ctx := business.WithVerifiedWorkloadPresenter(context.Background(), presenter)
			_, err := service.ModuleAuthorizeWorkContext(ctx, "alpha", token)
			require.ErrorIs(t, err, business.ErrExecutionIdentityMismatch)
		})
	}
	ctx := business.WithVerifiedWorkloadPresenter(context.Background(), valid)
	for range 3 {
		_, err := service.ModuleAuthorizeWorkContext(ctx, "alpha", token)
		require.NoError(t, err, "legitimate exchanges do not spend the projection")
	}
}
