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

// A sign-in whose single use cannot be recorded is REFUSED (handbook
// SP-IDENT-04). This replaces a test that asserted the opposite.
//
// The state here is otherwise entirely valid — correct signature, unexpired,
// right provider and redirect — so the only reason to refuse it is that the
// consumption could not be written. While the store is unavailable the question
// "has this been used before?" has no answer, and admitting on no answer means
// the single-use property does not hold for the duration of the outage.
func TestAStateWhoseSingleUseCannotBeRecordedIsRefused(t *testing.T) {
	s := newSigner(t, "seed")
	s.SetNonceConsumer(boomConsumer{})
	state, _ := s.Mint("workos", "https://x.example.com/cb")

	err := s.Verify(context.Background(), state, "workos", "https://x.example.com/cb")

	if !errors.Is(err, auth.ErrOAuthStateNotVerifiable) {
		t.Errorf("Verify = %v, want ErrOAuthStateNotVerifiable", err)
	}
	// A distinct error from a bad state, because an operator has to tell their
	// own dependency failing from someone presenting a replayed state. The
	// caller maps both to one sentinel before answering, so the client sees no
	// difference.
	if errors.Is(err, auth.ErrInvalidOAuthState) {
		t.Error("an outage must not be reported as an invalid state")
	}
}

// And the same signer admits that state once the store answers again: the
// refusal above is about the outage, not about the state.
func TestAValidStateIsAdmittedOnceTheStoreAnswers(t *testing.T) {
	s := newSigner(t, "seed")
	state, _ := s.Mint("workos", "https://x.example.com/cb")
	if err := s.Verify(context.Background(), state, "workos", "https://x.example.com/cb"); err != nil {
		t.Errorf("Verify = %v, want the state admitted", err)
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
