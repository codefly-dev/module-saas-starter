package business

import (
	"strings"
	"testing"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func TestRandomBase62_LengthAndAlphabet(t *testing.T) {
	for _, n := range []int{1, 16, 32, 100} {
		s, err := randomBase62(n)
		if err != nil {
			t.Fatalf("randomBase62(%d): %v", n, err)
		}
		if len(s) != n {
			t.Fatalf("randomBase62(%d) length = %d", n, len(s))
		}
		for i := 0; i < len(s); i++ {
			if !strings.ContainsRune(base62Alphabet, rune(s[i])) {
				t.Fatalf("character %q not in base62 alphabet", s[i])
			}
		}
	}
}

func TestRandomBase62_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		s, err := randomBase62(32)
		if err != nil {
			t.Fatalf("randomBase62: %v", err)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate 32-char key generated: %q", s)
		}
		seen[s] = struct{}{}
	}
}

// TestRandomBase62_NoModuloBias samples a large number of characters and
// asserts that no alphabet symbol is materially over-represented. A plain
// `b % 62` would over-weight the first eight symbols ("0"–"7"); rejection
// sampling keeps the distribution flat.
func TestRandomBase62_NoModuloBias(t *testing.T) {
	const total = 62 * 4000
	counts := make(map[rune]int, len(base62Alphabet))
	s, err := randomBase62(total)
	if err != nil {
		t.Fatalf("randomBase62: %v", err)
	}
	for _, r := range s {
		counts[r]++
	}
	if len(counts) != len(base62Alphabet) {
		t.Fatalf("expected all %d symbols to appear, saw %d", len(base62Alphabet), len(counts))
	}
	expected := float64(total) / float64(len(base62Alphabet))
	// Generous ±25% band: catches the ~2x skew a modulo bias would create
	// without flaking on ordinary sampling variance.
	low, high := expected*0.75, expected*1.25
	for _, r := range base62Alphabet {
		c := float64(counts[r])
		if c < low || c > high {
			t.Errorf("symbol %q appeared %d times, outside [%.0f, %.0f]", r, counts[r], low, high)
		}
	}
}
