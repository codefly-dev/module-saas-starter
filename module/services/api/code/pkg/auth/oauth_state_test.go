package auth_test

import (
	"strings"
	"testing"

	"api/pkg/auth"
)

func TestOAuthStateSigner_RoundTrip(t *testing.T) {
	s := auth.NewOAuthStateSigner([]byte("test-seed-1"))

	state, err := s.Mint("workos", "https://app.acme.com/auth/callback")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.Contains(state, ".") {
		t.Fatalf("state should be payload.sig, got %q", state)
	}

	if err := s.Verify(state, "workos", "https://app.acme.com/auth/callback"); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestOAuthStateSigner_TamperedSignature(t *testing.T) {
	s := auth.NewOAuthStateSigner([]byte("test-seed-1"))
	state, _ := s.Mint("workos", "https://x.example.com/cb")

	// Flip the last character of the signature.
	parts := strings.SplitN(state, ".", 2)
	parts[1] = parts[1][:len(parts[1])-1] + flipChar(parts[1][len(parts[1])-1])
	tampered := parts[0] + "." + parts[1]

	if err := s.Verify(tampered, "workos", "https://x.example.com/cb"); err == nil {
		t.Errorf("Verify should reject tampered signature")
	}
}

func TestOAuthStateSigner_ProviderMismatch(t *testing.T) {
	s := auth.NewOAuthStateSigner([]byte("test-seed-1"))
	state, _ := s.Mint("workos", "https://x.example.com/cb")

	// Same redirect, different provider — must reject so a state minted
	// for one IdP can't be replayed on another.
	if err := s.Verify(state, "google", "https://x.example.com/cb"); err == nil {
		t.Errorf("Verify should reject provider mismatch")
	}
}

func TestOAuthStateSigner_RedirectMismatch(t *testing.T) {
	s := auth.NewOAuthStateSigner([]byte("test-seed-1"))
	state, _ := s.Mint("workos", "https://x.example.com/cb")

	// Same provider, different redirect — must reject so an attacker
	// can't redirect the callback to their own URL with a stolen state.
	if err := s.Verify(state, "workos", "https://attacker.com/cb"); err == nil {
		t.Errorf("Verify should reject redirect mismatch")
	}
}

func TestOAuthStateSigner_DifferentKeys(t *testing.T) {
	a := auth.NewOAuthStateSigner([]byte("seed-A"))
	b := auth.NewOAuthStateSigner([]byte("seed-B"))

	state, _ := a.Mint("workos", "https://x/cb")
	if err := b.Verify(state, "workos", "https://x/cb"); err == nil {
		t.Errorf("state minted by A should not verify under B")
	}
}

func TestOAuthStateSigner_MalformedInput(t *testing.T) {
	s := auth.NewOAuthStateSigner([]byte("seed"))

	cases := []string{
		"",
		"no-dot",
		"too.many.dots",
		"!.!",
		strings.Repeat("a", 4096) + "." + strings.Repeat("b", 4096),
	}
	for _, c := range cases {
		if err := s.Verify(c, "workos", "https://x/cb"); err == nil {
			t.Errorf("Verify should reject malformed input: %q", c)
		}
	}
}

func flipChar(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}
