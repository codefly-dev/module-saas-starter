package auth_test

import (
	"strings"
	"testing"

	"api/pkg/auth"
)

// TestOAuthState_TamperedPayloadRejected exercises the signer at the
// "Authenticate would call Verify with a tampered state" boundary —
// the exact attack the server-side state validation is meant to
// stop.
//
// Lives alongside the unit tests but pinned at the package boundary
// (callers can only mint + verify) so it covers the same code path
// the business.Authenticate handler hits.
func TestOAuthState_TamperedPayloadRejected(t *testing.T) {
	signer := auth.NewOAuthStateSigner([]byte("integration-seed"))
	state, err := signer.Mint("workos", "https://app.example.com/cb")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	// Forger keeps the signature, swaps the payload to redirect the
	// callback through their own URL. Decoding base64 + re-encoding
	// new content produces a syntactically valid state token whose
	// signature won't match — exactly what Verify must catch.
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed state: %q", state)
	}
	// Replace payload with a clearly-different but valid base64url string.
	tampered := "ZXZpbA" + "." + parts[1]

	if err := signer.Verify(tampered, "workos", "https://app.example.com/cb"); err == nil {
		t.Errorf("Verify must reject tampered payload; accepted it")
	}
}

// TestOAuthState_ProviderConfusionRejected proves a state minted for
// a stronger IdP (workos with SSO) cannot be replayed on a weaker
// one (google with bare email) — would-be defense against an
// attacker who steals state via XSS in another tab.
func TestOAuthState_ProviderConfusionRejected(t *testing.T) {
	signer := auth.NewOAuthStateSigner([]byte("integration-seed"))
	workosState, _ := signer.Mint("workos", "https://app/cb")

	if err := signer.Verify(workosState, "google", "https://app/cb"); err == nil {
		t.Errorf("Verify must reject cross-provider replay")
	}
}

// TestOAuthState_RedirectHijackRejected proves a state cannot be
// reused with a different redirect_uri — the classic open-redirect
// attack where attacker swaps the callback to their domain.
func TestOAuthState_RedirectHijackRejected(t *testing.T) {
	signer := auth.NewOAuthStateSigner([]byte("integration-seed"))
	state, _ := signer.Mint("workos", "https://app.example.com/cb")

	for _, evil := range []string{
		"https://attacker.example.com/cb",
		"https://app.example.com/cb/extra",
		"http://app.example.com/cb",
		"",
	} {
		if err := signer.Verify(state, "workos", evil); err == nil {
			t.Errorf("Verify must reject redirect=%q", evil)
		}
	}
}
