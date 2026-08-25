package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
)

func TestOAuthStateSigner_RoundTrip(t *testing.T) {
	s := newSigner(t, "test-seed-1")

	state, err := s.Mint("workos", "https://app.acme.com/auth/callback")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.Contains(state, ".") {
		t.Fatalf("state should be payload.sig, got %q", state)
	}

	if err := s.Verify(context.Background(), state, "workos", "https://app.acme.com/auth/callback"); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestOAuthStateSigner_TamperedSignature(t *testing.T) {
	s := newSigner(t, "test-seed-1")
	state, _ := s.Mint("workos", "https://x.example.com/cb")

	// Flip the last character of the signature.
	parts := strings.SplitN(state, ".", 2)
	parts[1] = parts[1][:len(parts[1])-1] + flipChar(parts[1][len(parts[1])-1])
	tampered := parts[0] + "." + parts[1]

	if err := s.Verify(context.Background(), tampered, "workos", "https://x.example.com/cb"); err == nil {
		t.Errorf("Verify should reject tampered signature")
	}
}

func TestOAuthStateSigner_ProviderMismatch(t *testing.T) {
	s := newSigner(t, "test-seed-1")
	state, _ := s.Mint("workos", "https://x.example.com/cb")

	// Same redirect, different provider — must reject so a state minted
	// for one IdP can't be replayed on another.
	if err := s.Verify(context.Background(), state, "google", "https://x.example.com/cb"); err == nil {
		t.Errorf("Verify should reject provider mismatch")
	}
}

func TestOAuthStateSigner_RedirectMismatch(t *testing.T) {
	s := newSigner(t, "test-seed-1")
	state, _ := s.Mint("workos", "https://x.example.com/cb")

	// Same provider, different redirect — must reject so an attacker
	// can't redirect the callback to their own URL with a stolen state.
	if err := s.Verify(context.Background(), state, "workos", "https://attacker.com/cb"); err == nil {
		t.Errorf("Verify should reject redirect mismatch")
	}
}

func TestOAuthStateSigner_DifferentKeys(t *testing.T) {
	a := newSigner(t, "seed-A")
	b := newSigner(t, "seed-B")

	state, _ := a.Mint("workos", "https://x/cb")
	if err := b.Verify(context.Background(), state, "workos", "https://x/cb"); err == nil {
		t.Errorf("state minted by A should not verify under B")
	}
}

func TestOAuthStateSigner_MalformedInput(t *testing.T) {
	s := newSigner(t, "seed")

	cases := []string{
		"",
		"no-dot",
		"too.many.dots",
		"!.!",
		strings.Repeat("a", 4096) + "." + strings.Repeat("b", 4096),
	}
	for _, c := range cases {
		if err := s.Verify(context.Background(), c, "workos", "https://x/cb"); err == nil {
			t.Errorf("Verify should reject malformed input: %q", c)
		}
	}
}

func TestNewOAuthStateSigner_EmptySeedFailsClosed(t *testing.T) {
	if _, err := auth.NewOAuthStateSigner(nil); err == nil {
		t.Errorf("nil seed should be rejected, not silently given a random key")
	}
	if _, err := auth.NewOAuthStateSigner([]byte{}); err == nil {
		t.Errorf("empty seed should be rejected, not silently given a random key")
	}
}

func TestOAuthStateSigner_RejectsReplay(t *testing.T) {
	s := newSigner(t, "seed")
	state, err := s.Mint("workos", "https://x.example.com/cb")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := s.Verify(context.Background(), state, "workos", "https://x.example.com/cb"); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	// The same valid state replayed inside its TTL must be rejected: one-shot.
	if err := s.Verify(context.Background(), state, "workos", "https://x.example.com/cb"); err == nil {
		t.Errorf("Verify should reject a replayed state")
	}
}

type boomConsumer struct{}

func (boomConsumer) Consume(context.Context, string, time.Duration) (bool, error) {
	return false, errors.New("redis down")
}

func TestOAuthStateSigner_FailsOpenWhenConsumerErrors(t *testing.T) {
	s := newSigner(t, "seed")
	s.SetNonceConsumer(boomConsumer{})
	state, _ := s.Mint("workos", "https://x.example.com/cb")
	// The IdP's own single-use code is the authoritative anti-replay, so a
	// consumer-store outage must admit rather than break every OAuth login.
	if err := s.Verify(context.Background(), state, "workos", "https://x.example.com/cb"); err != nil {
		t.Errorf("Verify should fail open on consumer error, got %v", err)
	}
}

func newSigner(t *testing.T, seed string) *auth.OAuthStateSigner {
	t.Helper()
	s, err := auth.NewOAuthStateSigner([]byte(seed))
	if err != nil {
		t.Fatalf("NewOAuthStateSigner: %v", err)
	}
	return s
}

func flipChar(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

func TestOIDCNonceForState(t *testing.T) {
	// base64url(sha256("state-value")), no padding. Pinned so any change to the
	// derivation is caught here and mirrored in the frontend, which recomputes
	// the same value to send as the authorize `nonce`.
	const want = "prAw7QcteKLKykLonqMhVtJWjsKYigSNm2hM4ecezTs"
	if got := auth.OIDCNonceForState("state-value"); got != want {
		t.Fatalf("OIDCNonceForState = %q, want %q", got, want)
	}

	if a, b := auth.OIDCNonceForState("s1"), auth.OIDCNonceForState("s2"); a == b {
		t.Fatal("distinct states must derive distinct nonces")
	}
}
