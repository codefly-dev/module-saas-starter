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
		// RFC 6749 §5.2 answers invalid_grant with a 400; any other status is not that answer.
		{"invalid_grant off a 400 proves nothing", refused(500, "invalid_grant"), exchangeprobe.Unclassified, 500, "invalid_grant"},
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

// fakeProvider authenticates the client against accepted unless ignoresSecret,
// and records every exchange it was asked for.
type fakeProvider struct {
	accepted      string
	ignoresSecret bool
	secrets       []string
	codes         []string
	redirects     []string
}

type fakeExchanger struct {
	provider *fakeProvider
	secret   string
}

func (e fakeExchanger) Exchange(_ context.Context, code, redirectURI, codeVerifier string) (business.ExchangedTokens, error) {
	p := e.provider
	p.secrets, p.codes, p.redirects = append(p.secrets, e.secret), append(p.codes, code), append(p.redirects, redirectURI)
	if codeVerifier != "" {
		return business.ExchangedTokens{}, errors.New("the probe sends no verifier")
	}
	if !p.ignoresSecret && e.secret != p.accepted {
		return business.ExchangedTokens{}, refused(401, "invalid_client")
	}
	return business.ExchangedTokens{}, refused(400, "invalid_grant")
}

func (e fakeExchanger) WithClientSecret(secret string) business.CodeExchanger {
	return fakeExchanger{provider: e.provider, secret: secret}
}

func TestRunCertifiesOnlyAgainstTheNegativeControl(t *testing.T) {
	provider := &fakeProvider{accepted: "real-secret"}
	result := exchangeprobe.Run(context.Background(),
		fakeExchanger{provider: provider, secret: "real-secret"}, "https://app.example/callback")

	require.Equal(t, exchangeprobe.Result{
		Check: exchangeprobe.Check, Verdict: exchangeprobe.ClientAccepted, Status: 400,
		Error: "invalid_grant", Control: exchangeprobe.ClientRejected,
	}, result)
	require.Equal(t, []string{exchangeprobe.SentinelCode, exchangeprobe.SentinelCode}, provider.codes)
	require.Equal(t, []string{"https://app.example/callback", "https://app.example/callback"}, provider.redirects)
	require.Equal(t, "real-secret", provider.secrets[0])
	require.NotEqual(t, "real-secret", provider.secrets[1], "the control presents another secret")
	require.NotContains(t, provider.secrets[1], "real-secret", "and one not derived from the real one")
}

func TestRunLeavesUnprovenAProviderThatJudgesTheCodeFirst(t *testing.T) {
	// Answers invalid_grant whatever the secret: its answer says nothing about the client.
	provider := &fakeProvider{accepted: "real-secret", ignoresSecret: true}
	result := exchangeprobe.Run(context.Background(),
		fakeExchanger{provider: provider, secret: "wrong-secret"}, "")
	require.Equal(t, exchangeprobe.Unproven, result.Verdict)
	require.Equal(t, exchangeprobe.ClientAccepted, result.Control,
		"a random secret read as accepted is exactly why nothing is certified")
}

func TestRunNeedsNoControlForARejectedClient(t *testing.T) {
	provider := &fakeProvider{accepted: "real-secret"}
	result := exchangeprobe.Run(context.Background(),
		fakeExchanger{provider: provider, secret: "wrong-secret"}, "")
	require.Equal(t, exchangeprobe.ClientRejected, result.Verdict)
	require.Empty(t, result.Control)
	require.Len(t, provider.secrets, 1)
}

func TestRunCannotCertifyThroughAnExchangerWithoutAControl(t *testing.T) {
	result := exchangeprobe.Run(context.Background(), exchangerFunc(
		func(context.Context, string, string, string) (business.ExchangedTokens, error) {
			return business.ExchangedTokens{}, refused(400, "invalid_grant")
		}), "")
	require.Equal(t, exchangeprobe.Unproven, result.Verdict)
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
