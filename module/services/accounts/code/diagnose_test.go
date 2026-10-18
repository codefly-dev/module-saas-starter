package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"accounts/pkg/auth/exchangeprobe"

	"github.com/stretchr/testify/require"
)

// diagnosticChildArgs, when set, makes this test binary a child that runs the
// diagnostic its value names and exits — the only way to observe what reaches a
// caller's stdout and stderr once the diagnostic has redirected both.
const diagnosticChildArgs = "ACCOUNTS_TEST_DIAGNOSTIC_ARGS"

func TestDiagnosticChild(t *testing.T) {
	args := os.Getenv(diagnosticChildArgs)
	if args == "" {
		t.Skip("runs only as the child of a diagnostic test")
	}
	code, ran := runRequestedDiagnostic(strings.Fields(args))
	if !ran {
		code = 3
	}
	os.Exit(code)
}

// runDiagnosticChild runs `diagnose token-exchange` in a child process carrying
// this test's environment and returns everything it wrote to stdout and stderr.
func runDiagnosticChild(t *testing.T) string {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestDiagnosticChild$")
	child.Env = append(os.Environ(), diagnosticChildArgs+"=diagnose token-exchange")
	out, err := child.CombinedOutput()
	require.NoError(t, err, "child output: %s", out)
	return string(out)
}

// configureWorkOSAgainst points the identity configuration at idp with every
// endpoint pinned, so no discovery runs, and delivers secret the way a cell
// still holding it under the `identity` group does (which also logs a warning).
func configureWorkOSAgainst(t *testing.T, idp, secret string) {
	t.Helper()
	clearAuthProviderEnvironment(t)
	for _, group := range []string{"IDENTITY_PROVIDER", "IDENTITY-PROVIDER"} {
		t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__"+group+"__IDENTITY_CLIENT_SECRET", "")
		t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__"+group+"__IDENTITY_CLIENT_SECRET", "")
	}
	setIdentityConfiguration(t, "IDENTITY_PROVIDER", "workos")
	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID", "client_01TEST")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET", secret)
	setIdentityConfiguration(t, "IDENTITY_ISSUER", idp)
	setIdentityConfiguration(t, "IDENTITY_JWKS_URL", idp+"/jwks")
	setIdentityConfiguration(t, "IDENTITY_TOKEN_URL", idp+"/token")
	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID_CLAIM", "client_id")
	setIdentityConfiguration(t, "IDENTITY_ALLOWED_REDIRECT_URIS", "https://app.example/callback")
}

// fakeTokenEndpoint authenticates the client against accepted and refuses the
// sentinel code either way — with a body that quotes the presented secret, as
// the worst provider would, so a leak of the body is a leak of the secret.
func fakeTokenEndpoint(t *testing.T, accepted string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, exchangeprobe.SentinelCode, body["code"])
		w.Header().Set("Content-Type", "application/json")
		if body["client_secret"] != accepted {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"client secret ` + body["client_secret"] + ` is not valid"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"code unknown for secret ` + body["client_secret"] + `"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestTokenExchangeDiagnosticCertifiesAnAcceptedClient(t *testing.T) {
	const secret = "sk_test_diagnostic_secret_value_accepted"
	idp := fakeTokenEndpoint(t, secret)
	configureWorkOSAgainst(t, idp.URL, secret)

	out := runDiagnosticChild(t)
	require.Equal(t,
		`{"check":"token-exchange","verdict":"client-accepted","provider":"workos","status":400,"error":"invalid_grant"}`+"\n",
		out, "the result line and nothing else: no log line, no secret, no provider text")
}

func TestTokenExchangeDiagnosticReportsARejectedClientWithoutTheSecret(t *testing.T) {
	const secret = "sk_test_diagnostic_secret_value_rejected"
	idp := fakeTokenEndpoint(t, "the-secret-the-provider-expects")
	configureWorkOSAgainst(t, idp.URL, secret)

	out := runDiagnosticChild(t)
	require.Equal(t,
		`{"check":"token-exchange","verdict":"client-rejected","provider":"workos","status":401,"error":"invalid_client"}`+"\n",
		out)
	require.NotContains(t, out, secret)
	require.NotContains(t, out, "is not valid")
}

func TestTokenExchangeDiagnosticSendsNothingWhenTheStackDoesNotBuild(t *testing.T) {
	configureWorkOSAgainst(t, "https://identity.invalid", "")

	require.Equal(t,
		`{"check":"token-exchange","verdict":"not-configured","provider":"workos"}`+"\n",
		runDiagnosticChild(t))
}

func TestOnlyTheDiagnoseVerbLeavesTheServicePath(t *testing.T) {
	_, ran := runRequestedDiagnostic(nil)
	require.False(t, ran, "no arguments serve")
	_, ran = runRequestedDiagnostic([]string{"-test.v"})
	require.False(t, ran)

	code, ran := runRequestedDiagnostic([]string{"diagnose", "something-else"})
	require.True(t, ran, "an unknown diagnostic must never start the service")
	require.Equal(t, 2, code)
	code, ran = runRequestedDiagnostic([]string{"diagnose"})
	require.True(t, ran)
	require.Equal(t, 2, code)
}
