// Package exchangeprobe answers one question about the configured identity
// provider: does it accept this host's client authentication at the token
// endpoint?
//
// It asks by exchanging a sentinel authorization code through the very exchanger
// the login uses — same endpoint, same request shape, same credential. The
// provider cannot know the code, so a provider that authenticated the client
// refuses the grant (`invalid_grant`), and one that did not refuses the client
// (`invalid_client`, or a bare 401).
//
// RFC 6749 orders client authentication before the grant, but does not bind a
// provider to that order, and a provider that judges the code first answers
// `invalid_grant` whatever the secret. So an accepted client is certified only
// against a negative control: the same exchange presenting a random secret of
// the real one's shape must be refused as a client. A provider that answers both alike leaves the
// credential unproven.
//
// Nothing else is reported: the verdicts, the HTTP status and the OAuth error
// code. Never the credential, never the provider's response body.
package exchangeprobe

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"

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
	// ClientAccepted: the provider refused only the sentinel code (400
	// `invalid_grant`, RFC 6749 §5.2), and refused the same exchange as a client
	// when it presented a random secret: it accepts this client's credential. Only
	// client authentication is proven — not redirect registration, PKCE, or the
	// validation of a token a real code would return.
	ClientAccepted Verdict = "client-accepted"
	// Unproven: the provider refused only the code, but the negative control did
	// not show it authenticating the client — it answered a random secret alike,
	// or the exchanger cannot present one.
	Unproven Verdict = "unproven"
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
	// Control is how the provider answered the same exchange with a random secret;
	// set only when the real exchange's answer needed it.
	Control Verdict `json:"control,omitempty"`
}

// CredentialControl is an exchanger that can present another client secret with
// everything else unchanged — the negative control. derive receives the
// configured secret only to copy its shape; it never leaves the process.
type CredentialControl interface {
	WithClientSecretFrom(derive func(configured string) (string, error)) (business.CodeExchanger, error)
}

// Run exchanges the sentinel code through exchanger and classifies the answer,
// certifying an accepted client only against the negative control.
func Run(ctx context.Context, exchanger business.CodeExchanger, redirectURI string) Result {
	if exchanger == nil {
		return Result{Check: Check, Verdict: NoCodeFlow}
	}
	_, err := exchanger.Exchange(ctx, SentinelCode, redirectURI, "")
	result := Classify(err)
	if result.Verdict != ClientAccepted {
		return result
	}
	result.Verdict = Unproven
	control, ok := exchanger.(CredentialControl)
	if !ok {
		return result
	}
	controlExchanger, err := control.WithClientSecretFrom(controlSecretShapedLike)
	if err != nil {
		return result
	}
	_, err = controlExchanger.Exchange(ctx, SentinelCode, redirectURI, "")
	result.Control = Classify(err).Verdict
	if result.Control == ClientRejected {
		result.Verdict = ClientAccepted
	}
	return result
}

// controlSecretShapedLike returns a random secret with configured's shape: its
// prefix up to the last underscore (`sk_live_`) when that is no more than half
// of it, then, character for character, a random one of the same class —
// lower-case, upper-case or digit — with anything else kept. A provider that
// checks a secret's format before its value therefore cannot refuse the control
// on its shape alone. It never returns configured, and refuses a shape with too
// few random characters to be a control at all.
func controlSecretShapedLike(configured string) (string, error) {
	keep := strings.LastIndexByte(configured, '_') + 1
	if keep > len(configured)/2 {
		keep = 0
	}
	random := make([]byte, len(configured))
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	control, varied := []byte(configured), 0
	for i := keep; i < len(control); i++ {
		var class string
		switch c := control[i]; {
		case 'a' <= c && c <= 'z':
			class = "abcdefghijklmnopqrstuvwxyz"
		case 'A' <= c && c <= 'Z':
			class = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		case '0' <= c && c <= '9':
			class = "0123456789"
		default:
			continue
		}
		control[i] = class[int(random[i])%len(class)]
		varied++
	}
	if varied < minimumControlRandomCharacters || string(control) == configured {
		return "", errors.New("exchangeprobe: the configured secret leaves no room for a control")
	}
	return string(control), nil
}

// minimumControlRandomCharacters keeps a shaped control from colliding with a
// real secret by chance: 16 random characters is at least 53 bits even when
// every one is a digit.
const minimumControlRandomCharacters = 16

// Classify reads ONE exchange's error as a verdict; Run alone certifies, because
// ClientAccepted here means only "refused the grant, not the client" until the
// negative control agrees. A nil error — the provider accepted a code it never
// issued — proves nothing about the client and is unclassified; so is any
// failure that is not the provider's own answer.
func Classify(err error) Result {
	result := Result{Check: Check, Verdict: Unclassified}
	var refused *auth.TokenEndpointError
	switch {
	case errors.As(err, &refused):
		result.Status, result.Error = refused.Status, refused.Code
		switch {
		case refused.Code == "invalid_client" || refused.Status == 401:
			result.Verdict = ClientRejected
		case refused.Code == "invalid_grant" && refused.Status == 400:
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
