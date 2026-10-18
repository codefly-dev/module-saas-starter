package exchangeprobe_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/auth/exchangeprobe"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func refused(status int, code string) error {
	return fmt.Errorf("provider: %w", &auth.TokenEndpointError{Status: status, Code: code})
}

func TestClassifyReadsClientAuthenticationFromTheProvidersAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		verdict exchangeprobe.Verdict
		status  int
		code    string
	}{
		{"code refused after client auth", refused(400, "invalid_grant"), exchangeprobe.ClientAccepted, 400, "invalid_grant"},
		{"client refused as 401", refused(401, "invalid_client"), exchangeprobe.ClientRejected, 401, "invalid_client"},
		{"client refused as 400", refused(400, "invalid_client"), exchangeprobe.ClientRejected, 400, "invalid_client"},
		{"bare 401 without a code", refused(401, ""), exchangeprobe.ClientRejected, 401, ""},
		// invalid_grant never outweighs a 401: a provider that says both has
		// refused the client.
		{"401 with invalid_grant", refused(401, "invalid_grant"), exchangeprobe.ClientRejected, 401, "invalid_grant"},
		{"other code proves nothing", refused(400, "invalid_request"), exchangeprobe.Unclassified, 400, "invalid_request"},
		{"bare 400 proves nothing", refused(400, ""), exchangeprobe.Unclassified, 400, ""},
		{"server error proves nothing", refused(503, ""), exchangeprobe.Unclassified, 503, ""},
		{"transport failure", fmt.Errorf("x: %w", &url.Error{Op: "Post", URL: "https://idp.example", Err: errors.New("dial tcp: no such host")}), exchangeprobe.NoAnswer, 0, ""},
		{"timeout", fmt.Errorf("x: %w", context.DeadlineExceeded), exchangeprobe.NoAnswer, 0, ""},
		{"a sentinel code accepted", nil, exchangeprobe.Unclassified, 0, ""},
		{"failure after an accepted exchange", errors.New("validate access token"), exchangeprobe.Unclassified, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := exchangeprobe.Classify(tc.err)
			require.Equal(t, exchangeprobe.Result{
				Check: exchangeprobe.Check, Verdict: tc.verdict, Status: tc.status, Error: tc.code,
			}, got)
		})
	}
}

type exchangerFunc func(ctx context.Context, code, redirectURI, codeVerifier string) (business.ExchangedTokens, error)

func (f exchangerFunc) Exchange(ctx context.Context, code, redirectURI, codeVerifier string) (business.ExchangedTokens, error) {
	return f(ctx, code, redirectURI, codeVerifier)
}

func TestRunExchangesTheSentinelThroughTheGivenExchanger(t *testing.T) {
	var gotCode, gotRedirect, gotVerifier string
	result := exchangeprobe.Run(context.Background(), exchangerFunc(
		func(_ context.Context, code, redirectURI, codeVerifier string) (business.ExchangedTokens, error) {
			gotCode, gotRedirect, gotVerifier = code, redirectURI, codeVerifier
			return business.ExchangedTokens{}, refused(400, "invalid_grant")
		}), "https://app.example/callback")
	require.Equal(t, exchangeprobe.SentinelCode, gotCode)
	require.Equal(t, "https://app.example/callback", gotRedirect)
	require.Empty(t, gotVerifier)
	require.Equal(t, exchangeprobe.ClientAccepted, result.Verdict)
}

func TestRunWithoutAnExchangerHasNoCodeFlowToCertify(t *testing.T) {
	require.Equal(t, exchangeprobe.Result{Check: exchangeprobe.Check, Verdict: exchangeprobe.NoCodeFlow},
		exchangeprobe.Run(context.Background(), nil, ""))
}

func TestWritePrintsOneJSONLine(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, exchangeprobe.Write(&out, exchangeprobe.Result{
		Check: exchangeprobe.Check, Verdict: exchangeprobe.ClientRejected, Provider: "workos", Status: 401, Error: "invalid_client",
	}))
	require.Equal(t,
		`{"check":"token-exchange","verdict":"client-rejected","provider":"workos","status":401,"error":"invalid_client"}`+"\n",
		out.String())
}
