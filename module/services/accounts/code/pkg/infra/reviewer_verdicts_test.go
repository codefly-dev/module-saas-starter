//go:build pure

package infra

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/workloadidentity"
	"github.com/codefly-dev/core/workloadidentity/native"
	"github.com/stretchr/testify/require"
)

// The host tells a workload credential the review RAN and refused (a verdict:
// ErrExecutionTokenRejected) from a review that could not run (nothing was
// decided about the caller). The business layer reads only that sentinel, so
// each real reviewer must mark every verdict and nothing else. These tables
// pin both lists, reviewer by reviewer.

// tokenReviewAnswering is a Kubernetes reviewer whose API server answers a
// TokenReview with status and body, or fails with err.
func tokenReviewAnswering(t *testing.T, status int, body string, err error) *KubernetesClient {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host-token")
	require.NoError(t, os.WriteFile(path, []byte("host-test-token"), 0o600))
	return &KubernetesClient{endpoint: "https://api.example", tokenPath: path, http: &http.Client{Transport: reviewTransport(
		func(*http.Request) (*http.Response, error) {
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}}
}

func authenticated(username string, audiences string) string {
	return `{"status":{"authenticated":true,"audiences":` + audiences + `,"user":{"username":"` + username + `"}}}`
}

func TestEveryTokenReviewVerdictIsARejectionAndNothingElseIs(t *testing.T) {
	token := reviewToken(t, 5*time.Minute)
	const serviceAccount = "system:serviceaccount:acme-prod:shared"
	verdicts := map[string]struct {
		client *KubernetesClient
		token  string
	}{
		"no token":                  {tokenReviewAnswering(t, 200, authenticated(serviceAccount, `["accounts"]`), nil), ""},
		"not authenticated":         {tokenReviewAnswering(t, 200, `{"status":{"authenticated":false,"error":"token has expired"}}`, nil), token},
		"another audience":          {tokenReviewAnswering(t, 200, authenticated(serviceAccount, `["elsewhere"]`), nil), token},
		"not a service account":     {tokenReviewAnswering(t, 200, authenticated("jane@example.com", `["accounts"]`), nil), token},
		"malformed service account": {tokenReviewAnswering(t, 200, authenticated("system:serviceaccount:acme-prod", `["accounts"]`), nil), token},
		"lifetime unreadable":       {tokenReviewAnswering(t, 200, authenticated(serviceAccount, `["accounts"]`), nil), "an-opaque-token"},
	}
	for name, tc := range verdicts {
		t.Run("verdict/"+name, func(t *testing.T) {
			_, err := tc.client.ReviewToken(context.Background(), tc.token, "accounts")
			require.ErrorIs(t, err, business.ErrExecutionTokenRejected, "%v", err)
		})
	}

	unreadable := tokenReviewAnswering(t, 200, authenticated(serviceAccount, `["accounts"]`), nil)
	unreadable.tokenPath = filepath.Join(t.TempDir(), "missing")
	couldNotRun := map[string]*KubernetesClient{
		"transport failure":    tokenReviewAnswering(t, 0, "", errors.New("dial tcp 10.0.0.1:443: connect: connection refused")),
		"non-2xx answer":       tokenReviewAnswering(t, http.StatusForbidden, `{"kind":"Status"}`, nil),
		"unreadable body":      tokenReviewAnswering(t, 200, `{not json`, nil),
		"own token unreadable": unreadable,
	}
	for name, client := range couldNotRun {
		t.Run("could not run/"+name, func(t *testing.T) {
			_, err := client.ReviewToken(context.Background(), token, "accounts")
			require.Error(t, err)
			require.NotErrorIs(t, err, business.ErrExecutionTokenRejected, "a review that could not run decided nothing: %v", err)
			require.ErrorIs(t, err, ErrKubernetesUnavailable)
		})
	}
}

func TestEveryNativeAttestationVerdictIsARejectionAndNothingElseIs(t *testing.T) {
	declared := workloadidentity.Workload{TrustDomain: "cluster.local", Module: "reports", Service: "api"}
	undeclared := workloadidentity.Workload{TrustDomain: "cluster.local", Module: "billing", Service: "api"}
	issuer, err := native.NewIssuer()
	require.NoError(t, err)
	issuer.Declare(declared, undeclared)
	issue := func(i *native.Issuer, w workloadidentity.Workload) string {
		w.Instance = "api-1"
		token, err := i.Issue(native.Subject{Workload: w, Container: "api"}, "accounts")
		require.NoError(t, err)
		return token
	}
	// This host's anchor vouches for reports alone.
	anchor := issuer.Anchor()
	anchor.Subjects = []workloadidentity.Workload{declared}
	attester, err := native.NewAttester(anchor)
	require.NoError(t, err)
	identity := NewNativeWorkloadIdentity(attester)

	other, err := native.NewIssuer()
	require.NoError(t, err)
	other.Declare(declared)
	good := issue(issuer, declared)
	tampered := good[:len(good)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(good, "AA")]
	for name, credential := range map[string]string{
		"no credential":       "",
		"not a JWS":           "not-a-credential",
		"another run's key":   issue(other, declared),
		"tampered signature":  tampered,
		"undeclared workload": issue(issuer, undeclared),
	} {
		t.Run("verdict/"+name, func(t *testing.T) {
			_, err := identity.ReviewToken(context.Background(), credential, "accounts")
			require.ErrorIs(t, err, business.ErrExecutionTokenRejected, "%v", err)
		})
	}
	t.Run("verdict/another audience", func(t *testing.T) {
		_, err := identity.ReviewToken(context.Background(), good, "elsewhere")
		require.ErrorIs(t, err, business.ErrExecutionTokenRejected, "%v", err)
	})

	t.Run("could not run/no anchor", func(t *testing.T) {
		_, err := NewNativeWorkloadIdentity(nil).ReviewToken(context.Background(), good, "accounts")
		require.NotErrorIs(t, err, business.ErrExecutionTokenRejected, "%v", err)
		require.ErrorIs(t, err, ErrKubernetesUnavailable)
	})
	t.Run("could not run/anchor unreadable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "anchor.json")
		require.NoError(t, issuer.Anchor().WriteFile(path))
		t.Setenv(native.AnchorEnvironment, path)
		loaded, err := native.LoadAttester()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte("{rewritten and unreadable"), 0o600))
		_, err = NewNativeWorkloadIdentity(loaded).ReviewToken(context.Background(), good, "accounts")
		require.Error(t, err)
		require.NotErrorIs(t, err, business.ErrExecutionTokenRejected, "%v", err)
		require.ErrorIs(t, err, ErrKubernetesUnavailable)
	})
}
