package business

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"accounts/pkg/auth/egresstest"
)

// errWebhookDialStopped stands in for every connection in these tests, so the
// policy is judged on what it would dial and never dials anything.
var errWebhookDialStopped = errors.New("test: dial stopped before connect")

// The webhook policy refuses every non-public range, whether the address comes
// back from DNS at connect time or is written into the URL as a literal, and
// never dials a refused one. Unclassifiable dial inputs are not its input: it
// receives a URL's host, and the dial guards' tests hold those.
func TestWebhookEndpointPolicyRefusesEveryNonPublicRange(t *testing.T) {
	t.Parallel()
	for _, v := range egresstest.Refused {
		host := v.Host
		t.Run(v.Name, func(t *testing.T) {
			dialed := ""
			dial := func(_ context.Context, _, address string) (net.Conn, error) {
				dialed = address
				return nil, errWebhookDialStopped
			}
			answer := netip.MustParseAddr(host)
			viaDNS := &WebhookEndpointPolicy{resolver: &staticWebhookResolver{answers: []netip.Addr{answer}}, dial: dial}
			if _, err := viaDNS.dialContext(t.Context(), "tcp", "hooks.example.com:443"); err == nil || errors.Is(err, errWebhookDialStopped) || dialed != "" {
				t.Fatalf("DNS answer %s: err = %v, dialed %q; want refused before dialing", answer, err, dialed)
			}
			literal := &WebhookEndpointPolicy{resolver: &staticWebhookResolver{err: errors.New("must not resolve")}, dial: dial}
			if _, err := literal.dialContext(t.Context(), "tcp", net.JoinHostPort(host, "443")); err == nil || errors.Is(err, errWebhookDialStopped) || dialed != "" {
				t.Fatalf("literal %s: err = %v, dialed %q; want refused before dialing", host, err, dialed)
			}
		})
	}
}

// A public address is admitted and dialed as itself, on port 443.
func TestWebhookEndpointPolicyAdmitsPublicAddresses(t *testing.T) {
	t.Parallel()
	for _, v := range egresstest.Admitted {
		host := v.Host
		t.Run(v.Name, func(t *testing.T) {
			answer := netip.MustParseAddr(host)
			dialed := ""
			policy := &WebhookEndpointPolicy{
				resolver: &staticWebhookResolver{answers: []netip.Addr{answer}},
				dial: func(_ context.Context, _, address string) (net.Conn, error) {
					dialed = address
					return nil, errWebhookDialStopped
				},
			}
			if _, err := policy.dialContext(t.Context(), "tcp", "hooks.example.com:443"); !errors.Is(err, errWebhookDialStopped) {
				t.Fatalf("DNS answer %s: err = %v, want it admitted and dialed", answer, err)
			}
			if want := net.JoinHostPort(answer.Unmap().String(), "443"); dialed != want {
				t.Fatalf("dialed %q, want %q", dialed, want)
			}
		})
	}
}
