package business

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type staticWebhookResolver struct {
	answers []netip.Addr
	err     error
	calls   int
}

func (r *staticWebhookResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	r.calls++
	return append([]netip.Addr(nil), r.answers...), r.err
}

type sequenceWebhookResolver struct {
	answers [][]netip.Addr
	calls   int
}

func (r *sequenceWebhookResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	index := r.calls
	r.calls++
	if index >= len(r.answers) {
		index = len(r.answers) - 1
	}
	return append([]netip.Addr(nil), r.answers[index]...), nil
}

func TestWebhookEndpointPolicyRejectsNonPublicAndAmbiguousAddresses(t *testing.T) {
	t.Parallel()
	policy := (&WebhookEndpointPolicy{}).ensureDefaults()
	tests := []string{
		"http://example.com/hook",
		"https://127.0.0.1/hook",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.1/hook",
		"https://100.64.0.1/hook",
		"https://192.0.2.1/hook",
		"https://198.18.0.1/hook",
		"https://[::1]/hook",
		"https://[fe80::1]/hook",
		"https://[fc00::1]/hook",
		"https://[2001:db8::1]/hook",
		"https://[::ffff:127.0.0.1]/hook",
		"https://2130706433/hook",
		"https://0177.0.0.1/hook",
		"https://0x7f000001/hook",
		"https://user:password@example.com/hook",
		"https://example.com:8443/hook",
		"https://example.com/hook#fragment",
		"https://localhost/hook",
	}
	for _, rawURL := range tests {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			if normalized, err := policy.NormalizeAndValidate(t.Context(), rawURL); err == nil {
				t.Fatalf("NormalizeAndValidate(%q) = %q, want rejection", rawURL, normalized)
			}
		})
	}
}

func TestWebhookEndpointPolicyNormalizesAndRejectsMixedDNS(t *testing.T) {
	t.Parallel()
	public := netip.MustParseAddr("93.184.216.34")
	resolver := &staticWebhookResolver{answers: []netip.Addr{public}}
	policy := &WebhookEndpointPolicy{resolver: resolver}
	normalized, err := policy.NormalizeAndValidate(t.Context(), "https://EXAMPLE.COM.:443/hooks?source=test")
	if err != nil {
		t.Fatalf("NormalizeAndValidate: %v", err)
	}
	if normalized != "https://example.com/hooks?source=test" {
		t.Fatalf("normalized URL = %q", normalized)
	}

	policy = &WebhookEndpointPolicy{resolver: &staticWebhookResolver{answers: []netip.Addr{
		public,
		netip.MustParseAddr("10.1.2.3"),
	}}}
	if _, err := policy.NormalizeAndValidate(t.Context(), "https://example.com/hook"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("mixed DNS answer error = %v", err)
	}
}

func TestWebhookEndpointPolicyRevalidatesDNSAtConnectAndPinsAddress(t *testing.T) {
	t.Parallel()
	public := netip.MustParseAddr("93.184.216.34")
	resolver := &sequenceWebhookResolver{answers: [][]netip.Addr{
		{public},
		{netip.MustParseAddr("169.254.169.254")},
	}}
	dialed := false
	policy := &WebhookEndpointPolicy{
		resolver: resolver,
		dial: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, fmt.Errorf("must not dial")
		},
	}
	if _, err := policy.NormalizeAndValidate(t.Context(), "https://hooks.example.com/events"); err != nil {
		t.Fatalf("registration validation: %v", err)
	}
	if _, err := policy.dialContext(t.Context(), "tcp", "hooks.example.com:443"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("connect-time error = %v", err)
	}
	if dialed {
		t.Fatal("dial was attempted after DNS rebound to metadata address")
	}

	resolver = &sequenceWebhookResolver{answers: [][]netip.Addr{{public}}}
	var dialAddress string
	var peer net.Conn
	policy = &WebhookEndpointPolicy{
		resolver: resolver,
		dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dialAddress = address
			client, server := net.Pipe()
			peer = server
			return client, nil
		},
	}
	conn, err := policy.dialContext(t.Context(), "tcp", "hooks.example.com:443")
	if err != nil {
		t.Fatalf("dialContext: %v", err)
	}
	_ = conn.Close()
	_ = peer.Close()
	if dialAddress != "93.184.216.34:443" {
		t.Fatalf("dial address = %q, want validated IP", dialAddress)
	}
}

type webhookRoundTripFunc func(*http.Request) (*http.Response, error)

func (f webhookRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestWebhookHTTPClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	client := NewWebhookEndpointPolicy().HTTPClient()
	calls := 0
	client.Transport = webhookRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://169.254.169.254/latest/meta-data"}},
			Body:       io.NopCloser(strings.NewReader("redirect refused")),
			Request:    req,
		}, nil
	})
	resp, err := client.Get("https://example.com/hook")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if calls != 1 || resp.StatusCode != http.StatusFound {
		t.Fatalf("calls/status = %d/%d, want 1/302", calls, resp.StatusCode)
	}
}

type memoryWebhookCipher struct {
	mu      sync.Mutex
	values  map[string]string
	purpose map[string]string
	next    int
}

func newMemoryWebhookCipher() *memoryWebhookCipher {
	return &memoryWebhookCipher{values: map[string]string{}, purpose: map[string]string{}}
}

func (c *memoryWebhookCipher) EncryptSecret(_ context.Context, purpose, plaintext string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	envelope := fmt.Sprintf("encrypted-%d", c.next)
	c.values[envelope] = plaintext
	c.purpose[envelope] = purpose
	return envelope, nil
}

func (c *memoryWebhookCipher) DecryptSecret(_ context.Context, purpose, envelope string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.purpose[envelope] != purpose {
		return "", fmt.Errorf("purpose mismatch")
	}
	return c.values[envelope], nil
}

type memoryWebhookStore struct {
	Store
	current *WebhookSubscription
}

func (s *memoryWebhookStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *memoryWebhookStore) CreateWebhookSubscription(_ context.Context, sub *WebhookSubscription) error {
	copy := *sub
	copy.Events = append([]string(nil), sub.Events...)
	s.current = &copy
	return nil
}

func (s *memoryWebhookStore) GetWebhookSubscription(_ context.Context, id string) (*WebhookSubscription, error) {
	if s.current == nil || s.current.ID != id {
		return nil, nil
	}
	copy := *s.current
	return &copy, nil
}

func (s *memoryWebhookStore) UpdateWebhookSubscription(_ context.Context, sub *WebhookSubscription) error {
	copy := *sub
	s.current = &copy
	return nil
}

func TestCreateAndRotateWebhookSecretIsEncryptedAndRevealedOnce(t *testing.T) {
	t.Parallel()
	store := &memoryWebhookStore{}
	cipher := newMemoryWebhookCipher()
	policy := &WebhookEndpointPolicy{resolver: &staticWebhookResolver{answers: []netip.Addr{
		netip.MustParseAddr("93.184.216.34"),
	}}}
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.SetWebhookSecurity(cipher, policy)

	created, err := service.CreateSubscription(
		t.Context(),
		"00000000-0000-0000-0000-000000000001",
		"https://HOOKS.EXAMPLE.COM:443/events",
		[]string{"user.created"},
		"production consumer",
	)
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if !strings.HasPrefix(created.SecretReveal, "whsec_") {
		t.Fatalf("secret reveal = %q", created.SecretReveal)
	}
	if created.SecretEncrypted == "" || created.SecretEncrypted == created.SecretReveal {
		t.Fatalf("stored secret must be a non-plaintext envelope")
	}
	if store.current.SecretReveal != created.SecretReveal {
		// The repository object carries the transient field, but Postgres never
		// persists it. Clear our fake to model a later database read.
		t.Fatalf("fake create did not capture subscription")
	}
	store.current.SecretReveal = ""
	oldEnvelope := store.current.SecretEncrypted

	rotated, expiresAt, err := service.RotateWebhookSecret(t.Context(), created.OrgID, created.ID, 24*time.Hour)
	if err != nil {
		t.Fatalf("RotateWebhookSecret: %v", err)
	}
	if rotated == created.SecretReveal || !strings.HasPrefix(rotated, "whsec_") {
		t.Fatalf("rotated secret was not freshly generated")
	}
	if expiresAt == nil || time.Until(*expiresAt) < 23*time.Hour {
		t.Fatalf("rotation expiry = %v", expiresAt)
	}
	if store.current.PreviousSecretEncrypted != oldEnvelope {
		t.Fatalf("previous envelope = %q, want %q", store.current.PreviousSecretEncrypted, oldEnvelope)
	}
	if store.current.SecretEncrypted == oldEnvelope || store.current.SecretEncrypted == rotated {
		t.Fatal("new secret was not replaced with a fresh encrypted envelope")
	}

	// A later read never reconstructs the one-time plaintext reveal.
	loaded, err := store.GetWebhookSubscription(t.Context(), created.ID)
	if err != nil || loaded.SecretReveal != "" {
		t.Fatalf("later secret reveal = %q, err=%v", loaded.SecretReveal, err)
	}
}

func TestWebhookEventTypesUseCanonicalRoutingNames(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"user.created", "api_key.revoked", "plugin-event.v2"} {
		if !canonicalWebhookEventType.MatchString(event) {
			t.Fatalf("canonical event %q was rejected", event)
		}
	}
	for _, event := range []string{"", "User.Created", "user created", "user.created\nforged"} {
		if canonicalWebhookEventType.MatchString(event) {
			t.Fatalf("non-canonical event %q was accepted", event)
		}
	}
}
