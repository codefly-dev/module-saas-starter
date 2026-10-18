//go:build pure

package infra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"accounts/pkg/infra"

	"github.com/stretchr/testify/require"
)

// Carrier authorisation: who may HAND a manifest to this host, as opposed to who
// signed it.
//
// Every test here is a way the host could accept a manifest from a caller that
// is not its carrier: the `delivery` ServiceAccount of the namespace the cell
// runs the manifest's module in.

// tokenReviewing stands up an API server that answers TokenReview with a given
// username and audiences, and records what it was asked.
//
// A fake API server rather than a fake client, deliberately: the thing worth
// proving includes that the AUDIENCE is sent, and a fake client would let that
// go untested while looking identical.
func tokenReviewing(t *testing.T, username string, audiences []string, authenticated bool) (*infra.KubernetesClient, *tokenReviewProbe) {
	t.Helper()
	probe := &tokenReviewProbe{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Spec struct {
				Token     string   `json:"token"`
				Audiences []string `json:"audiences"`
			} `json:"spec"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		probe.token = request.Spec.Token
		probe.audiences = request.Spec.Audiences
		probe.authorization = r.Header.Get("Authorization")

		response := map[string]any{"status": map[string]any{
			"authenticated": authenticated,
			"audiences":     audiences,
			"user":          map[string]any{"username": username},
		}}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(server.Close)

	client := infra.NewKubernetesClientForTest(t, server)
	return client, probe
}

type tokenReviewProbe struct {
	token         string
	audiences     []string
	authorization string
}

func TestAppliedObserverIsASeparateReadIdentity(t *testing.T) {
	for _, tc := range []struct {
		username string
		audience string
		want     error
	}{
		{"system:serviceaccount:istio-presenter:endpoint-publisher", "accounts", nil},
		{"system:serviceaccount:other:endpoint-publisher", "accounts", infra.ErrCarrierNotAuthorized},
		{"system:serviceaccount:istio-presenter:delivery", "accounts", infra.ErrCarrierNotAuthorized},
		{"system:serviceaccount:acme-crm:delivery", "accounts", infra.ErrCarrierNotAuthorized},
		{"system:serviceaccount:istio-presenter:endpoint-publisher", "kubernetes", infra.ErrCarrierUnauthenticated},
	} {
		t.Run(tc.username+"/"+tc.audience, func(t *testing.T) {
			client, probe := tokenReviewing(t, tc.username, []string{tc.audience}, true)
			check := infra.NewSolutionDeliveryCarrierCheck(client)
			err := check.AuthorizeAppliedObserver(context.Background(), "Bearer publisher-token")
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
			require.Equal(t, []string{"accounts"}, probe.audiences)
			require.Equal(t, "publisher-token", probe.token)
			if tc.want == nil {
				require.ErrorIs(t, check.AuthorizeCarrier(context.Background(), "Bearer publisher-token", "istio-presenter"), infra.ErrCarrierNotAuthorized)
			}
		})
	}
	client, _ := tokenReviewing(t, "", nil, false)
	require.ErrorIs(t, infra.NewSolutionDeliveryCarrierCheck(client).AuthorizeAppliedObserver(context.Background(), "Bearer refused"), infra.ErrCarrierUnauthenticated)
	require.ErrorIs(t, infra.NewSolutionDeliveryCarrierCheck(client).AuthorizeAppliedObserver(context.Background(), ""), infra.ErrCarrierUnauthenticated)
	require.ErrorIs(t, infra.NewSolutionDeliveryCarrierCheck(nil).AuthorizeAppliedObserver(context.Background(), "Bearer token"), infra.ErrKubernetesUnavailable)
}

// The AUDIENCE is sent on every review. A TokenReview without one is checked
// against the API server's own audiences, which would let the token every pod
// is given for talking to the API server authenticate here.
func TestCarrierReviewSendsTheAudience(t *testing.T) {
	client, probe := tokenReviewing(t,
		"system:serviceaccount:acme-crm:delivery", []string{"accounts"}, true)
	check := infra.NewSolutionDeliveryCarrierCheck(client)

	require.NoError(t, check.AuthorizeCarrier(
		context.Background(), "Bearer caller-token", "acme-crm"))
	require.Equal(t, []string{"accounts"}, probe.audiences,
		"an audience-less review accepts a token minted for the api server itself")
	require.Equal(t, "caller-token", probe.token)
	require.NotEmpty(t, probe.authorization,
		"the host presents its OWN credential to the api server")
}

// The carrier pair is (the module's namespace, delivery), and both halves of it
// are checked.
func TestCarrierRequiresTheDeliveryAccountOfTheNamespace(t *testing.T) {
	for name, username := range map[string]string{
		"wrong namespace":       "system:serviceaccount:platform:delivery",
		"wrong service account": "system:serviceaccount:acme-crm:publisher",
		"both wrong":            "system:serviceaccount:default:default",
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := tokenReviewing(t, username, []string{"accounts"}, true)
			check := infra.NewSolutionDeliveryCarrierCheck(client)
			err := check.AuthorizeCarrier(
				context.Background(), "Bearer t", "acme-crm")
			require.ErrorIs(t, err, infra.ErrCarrierNotAuthorized)
		})
	}
}

// A carrier from the module's own namespace is authorised; the same carrier is
// refused for a manifest of another module. Both directions in one test, so it
// cannot pass by refusing everything.
func TestCarrierIsCheckedAgainstTheModulesNamespace(t *testing.T) {
	client, _ := tokenReviewing(t,
		"system:serviceaccount:acme-crm:delivery", []string{"accounts"}, true)
	check := infra.NewSolutionDeliveryCarrierCheck(client)

	require.NoError(t, check.AuthorizeCarrier(
		context.Background(), "Bearer t", "acme-crm"),
		"a carrier from the module's namespace is its delivery account")

	err := check.AuthorizeCarrier(
		context.Background(), "Bearer t", "acme-other")
	require.ErrorIs(t, err, infra.ErrCarrierNotAuthorized,
		"the same carrier must not deliver another module's manifest")
}

// An empty namespace authorises no carrier: a reviewed account always names one.
func TestCarrierForNoNamespaceIsRefused(t *testing.T) {
	client, _ := tokenReviewing(t,
		"system:serviceaccount:acme-crm:delivery", []string{"accounts"}, true)
	err := infra.NewSolutionDeliveryCarrierCheck(client).AuthorizeCarrier(context.Background(), "Bearer t", "")
	require.ErrorIs(t, err, infra.ErrCarrierNotAuthorized)
}

// A token the API server REFUSED is terminal and distinct from one it could not
// review. The token is kubelet-projected and re-read per request, so a refusal
// is a configuration fault and a retry asks the same question.
func TestCarrierRefusedTokenIsUnauthenticated(t *testing.T) {
	client, _ := tokenReviewing(t, "", nil, false)
	check := infra.NewSolutionDeliveryCarrierCheck(client)

	err := check.AuthorizeCarrier(
		context.Background(), "Bearer t", "acme-crm")
	require.ErrorIs(t, err, infra.ErrCarrierUnauthenticated)
	require.NotErrorIs(t, err, infra.ErrKubernetesUnavailable,
		"a refusal is a verdict, never an outage")
}

// A token valid for ANOTHER audience is refused, even though the API server
// authenticated it. This is the check that stops a token its holder was
// legitimately given for a different service from delivering documents.
func TestCarrierTokenForAnotherAudienceIsRefused(t *testing.T) {
	client, _ := tokenReviewing(t,
		"system:serviceaccount:acme-crm:delivery", []string{"some-other-service"}, true)
	check := infra.NewSolutionDeliveryCarrierCheck(client)

	err := check.AuthorizeCarrier(
		context.Background(), "Bearer t", "acme-crm")
	require.ErrorIs(t, err, infra.ErrCarrierUnauthenticated)
}

// An identity that is not a service account is refused rather than read as a
// service account in the "" namespace — which, for the presence half where the
// namespace comes from the document, could compare equal to nothing by accident.
func TestCarrierNonServiceAccountIdentityIsRefused(t *testing.T) {
	for _, username := range []string{"jane@example.com", "system:node:worker-1", "", "system:serviceaccount:", "system:serviceaccount:ns:"} {
		client, _ := tokenReviewing(t, username, []string{"accounts"}, true)
		check := infra.NewSolutionDeliveryCarrierCheck(client)
		err := check.AuthorizeCarrier(
			context.Background(), "Bearer t", "acme-crm")
		require.ErrorIs(t, err, infra.ErrCarrierUnauthenticated, "username %q", username)
	}
}

// No credential at all is refused before the API server is asked.
func TestCarrierWithoutATokenIsRefused(t *testing.T) {
	client, probe := tokenReviewing(t,
		"system:serviceaccount:acme-crm:delivery", []string{"accounts"}, true)
	check := infra.NewSolutionDeliveryCarrierCheck(client)

	for _, credential := range []string{"", "   ", "Basic abc", "Bearer", "Bearer a b"} {
		err := check.AuthorizeCarrier(
			context.Background(), credential, "acme-crm")
		require.ErrorIs(t, err, infra.ErrCarrierUnauthenticated, "credential %q", credential)
	}
	require.Empty(t, probe.token, "the api server must not be asked about a credential that is not a bearer token")
}

// `bearer` lowercase is accepted: HTTP says the scheme is case-insensitive, and
// a caller sending it would otherwise be refused for a reason no error message
// would make obvious.
func TestCarrierBearerSchemeIsCaseInsensitive(t *testing.T) {
	client, _ := tokenReviewing(t,
		"system:serviceaccount:acme-crm:delivery", []string{"accounts"}, true)
	check := infra.NewSolutionDeliveryCarrierCheck(client)

	require.NoError(t, check.AuthorizeCarrier(
		context.Background(), "bearer t", "acme-crm"))
}

// An unreachable API server is RETRYABLE and says nothing about the caller. A
// verifier that concluded "not authorised" from its own inability to ask would
// be reporting a verification it did not perform.
func TestCarrierUnreachableApiServerIsNotAVerdict(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	check := infra.NewSolutionDeliveryCarrierCheck(infra.NewKubernetesClientForTest(t, server))

	err := check.AuthorizeCarrier(
		context.Background(), "Bearer t", "acme-crm")
	require.ErrorIs(t, err, infra.ErrKubernetesUnavailable)
	require.NotErrorIs(t, err, infra.ErrCarrierUnauthenticated)
	require.NotErrorIs(t, err, infra.ErrCarrierNotAuthorized)
}

// A host with no way to review carriers accepts documents from nobody.
func TestCarrierCheckWithoutAClientFailsClosed(t *testing.T) {
	check := infra.NewSolutionDeliveryCarrierCheck(nil)
	err := check.AuthorizeCarrier(
		context.Background(), "Bearer t", "acme-crm")
	require.ErrorIs(t, err, infra.ErrKubernetesUnavailable)
}

// The host re-reads its OWN token per request rather than caching it at boot.
//
// A projected service-account token is rotated by the kubelet at ~80% of its
// lifetime, so a value captured at boot stops working some hours later — which
// presents as the API server refusing the host's own identity, the single most
// confusing failure available here.
func TestHostTokenIsReReadPerRequest(t *testing.T) {
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("first"), 0o600))

	seen := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{
			"authenticated": true,
			"audiences":     []string{"accounts"},
			"user":          map[string]any{"username": "system:serviceaccount:acme-crm:delivery"},
		}}))
	}))
	t.Cleanup(server.Close)

	client := infra.NewKubernetesClientForTestWithToken(t, server, tokenPath)
	check := infra.NewSolutionDeliveryCarrierCheck(client)

	require.NoError(t, check.AuthorizeCarrier(
		context.Background(), "Bearer t", "acme-crm"))
	require.NoError(t, os.WriteFile(tokenPath, []byte("rotated"), 0o600))
	require.NoError(t, check.AuthorizeCarrier(
		context.Background(), "Bearer t", "acme-crm"))

	require.Equal(t, []string{"Bearer first", "Bearer rotated"}, seen,
		"the host's own token must be re-read per request, or it stops working when the kubelet rotates it")
}
