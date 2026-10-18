// Package exchangeprobe answers one question about the configured identity
// provider: does it accept this host's client authentication at the token
// endpoint?
//
// It asks by exchanging a sentinel authorization code through the very exchanger
// the login uses — same endpoint, same request shape, same credential. The
// provider cannot know the code, so a provider that authenticated the client
// refuses the grant (`invalid_grant`), and one that did not refuses the client
// (`invalid_client`, or a bare 401). Nothing else is reported: the verdict, the
// HTTP status and the OAuth error code. Never the credential, never the
// provider's response body.
package exchangeprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"

	"accounts/pkg/auth"
	"accounts/pkg/business"
)

// Check names this diagnostic on the command line and in its result.
const Check = "token-exchange"

// SentinelCode is the authorization code the probe exchanges. No provider
// issued it, so no exchange of it can succeed.
const SentinelCode = "token-exchange-probe-sentinel"

// Verdict is what the provider's answer establishes about client authentication.
type Verdict string

const (
	// ClientAccepted: the provider authenticated the client and refused only the
	// sentinel code (RFC 6749 §5.2 `invalid_grant`). A real code would exchange.
	ClientAccepted Verdict = "client-accepted"
	// ClientRejected: the provider refused the client itself — `invalid_client`,
	// or a 401. Every login's code exchange fails the same way.
	ClientRejected Verdict = "client-rejected"
	// Unclassified: the provider answered, but with nothing that proves either.
	Unclassified Verdict = "unclassified"
	// NoAnswer: the request got no HTTP answer (DNS, connect, timeout).
	NoAnswer Verdict = "no-answer"
	// NotConfigured: the identity configuration does not build a provider stack,
	// so this process would refuse to start; nothing was sent.
	NotConfigured Verdict = "not-configured"
	// NoCodeFlow: the configured provider performs no code exchange (a fixture or
	// a gateway-asserted identity), so there is nothing to certify.
	NoCodeFlow Verdict = "no-code-flow"
)

// Result is the whole of what the diagnostic reports.
type Result struct {
	Check    string  `json:"check"`
	Verdict  Verdict `json:"verdict"`
	Provider string  `json:"provider,omitempty"`
	Status   int     `json:"status,omitempty"`
	Error    string  `json:"error,omitempty"`
}

// Run exchanges the sentinel code through exchanger and classifies the answer.
func Run(ctx context.Context, exchanger business.CodeExchanger, redirectURI string) Result {
	if exchanger == nil {
		return Result{Check: Check, Verdict: NoCodeFlow}
	}
	_, err := exchanger.Exchange(ctx, SentinelCode, redirectURI, "")
	return Classify(err)
}

// Classify reads an exchange's error as a verdict. A nil error — the provider
// accepted a code it never issued — proves nothing about the client and is
// unclassified; so is any failure that is not the provider's own answer.
func Classify(err error) Result {
	result := Result{Check: Check, Verdict: Unclassified}
	var refused *auth.TokenEndpointError
	switch {
	case errors.As(err, &refused):
		result.Status, result.Error = refused.Status, refused.Code
		switch {
		case refused.Code == "invalid_client" || refused.Status == 401:
			result.Verdict = ClientRejected
		case refused.Code == "invalid_grant":
			result.Verdict = ClientAccepted
		}
	case err != nil && unanswered(err):
		result.Verdict = NoAnswer
	}
	return result
}

func unanswered(err error) bool {
	var transport *url.Error
	var network net.Error
	return errors.As(err, &transport) || errors.As(err, &network) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// Write prints result as the single JSON line a caller parses.
func Write(w io.Writer, result Result) error {
	line, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}
