package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// R1019-N12: one origin must have one spelling on BOTH sides of the comparison.
//
// The frontend canonicalizes with `new URL(...).origin`, the WHATWG parser; this
// service canonicalizes with net/url, which is laxer. Where they disagreed, a
// configured origin and the equivalent origin the frontend resolved compared unequal,
// and the refusal looked like a misconfiguration.
//
// These vectors are the agreement itself. The same table is asserted in
// src/test/public-origin-canonicalization.test.ts against the real URL parser, and
// module/tools/public_origin_vectors_lockstep_test.go holds the two copies identical
// — so a case fixed on one side cannot be left failing on the other. Each side
// asserts BEHAVIOUR on the vectors; the lockstep gate only proves both assert the
// same set.
//
// Expected values are the browser's own answers, taken from the parser rather than
// reasoned about, with the deliberate exceptions noted in refusedPublicOriginVectors.
//
// THIS TABLE IS THE FULL EXTENT OF THE AGREEMENT. The lockstep gate proves the two
// sides assert the same cases; it cannot prove anything about a case absent from both,
// and a gap here reads as agreement when there is none. An underscore host and a
// hyphen-edged label were both missing, and both were spellings the two sides answered
// differently (R1019-N12/f3). A new spelling belongs here first.
var canonicalPublicOriginVectors = []struct{ Input, Canonical string }{
	{"https://app.example", "https://app.example"},
	{"https://APP.example", "https://app.example"},
	{"https://app.example:443", "https://app.example"},
	{"https://app.example:0443", "https://app.example"},
	{"https://app.example:", "https://app.example"},
	{"https://app.example:8443", "https://app.example:8443"},
	{"https://app.example:08443", "https://app.example:8443"},
	{"https://app.example/", "https://app.example"},
	{"https://[0:0:0:0:0:0:0:1]", "https://[::1]"},
	{"https://[::1]:443", "https://[::1]"},
	{"https://bücher.example", "https://xn--bcher-kva.example"},
	{"http://localhost:80", "http://localhost"},
	{"http://127.0.0.1:3000", "http://127.0.0.1:3000"},
	{"http://[::1]:80", "http://[::1]"},
	{"https://my_app.example", "https://my_app.example"},
	{"https://a_b-c.example", "https://a_b-c.example"},
	{"https://BÜCHER.example", "https://xn--bcher-kva.example"},
}

// Refused on both sides. Every entry but one is refused by the browser parser too.
//
// The exception is port 0, which `new URL` accepts and keeps. It is refused here
// because no origin can be served on it, and a rule both sides apply is worth more
// than matching the parser on a value that cannot occur: the frontend's
// canonicalOrigin applies the same range check, so the two still agree.
var refusedPublicOriginVectors = []string{
	"https://:443",
	"https://app.example:99999",
	"https://app.example:0",
	"https://-app.example",
	"https://app-.example",
	"https://app.example:-1",
	"https://app.example/path",
	"https://app.example?q=1",
	"https://app.example#f",
	"https://user:secret@app.example",
	"app.example",
	"",
}

func TestR1019CanonicalPublicOriginMatchesTheBrowserSpelling(t *testing.T) {
	for _, vector := range canonicalPublicOriginVectors {
		t.Run(vector.Input, func(t *testing.T) {
			got, err := CanonicalPublicOrigin(vector.Input)
			require.NoError(t, err, "%q is a usable origin", vector.Input)
			require.Equal(t, vector.Canonical, got,
				"%q must canonicalize the way the browser spells it", vector.Input)
		})
	}
}

func TestR1019CanonicalPublicOriginRefusesWhatTheBrowserWillNotParse(t *testing.T) {
	for _, input := range refusedPublicOriginVectors {
		t.Run(input, func(t *testing.T) {
			_, err := CanonicalPublicOrigin(input)
			require.Error(t, err, "%q is not an origin and must be refused", input)
		})
	}
}

// Canonicalization is only worth anything if the COMPARISON uses it. A configured
// origin and a differently spelled equivalent must reach the same verified origin.
func TestR1019ConfiguredOriginAcceptsEveryEquivalentSpelling(t *testing.T) {
	t.Cleanup(func() { _ = SetConfiguredPublicOrigin("") })

	for _, spelling := range []string{
		"https://app.example", "https://APP.example", "https://app.example:443",
		"https://app.example:0443", "https://app.example:", "https://app.example/",
	} {
		require.NoError(t, SetConfiguredPublicOrigin(spelling))
		configured, ok := ConfiguredPublicOrigin()
		require.True(t, ok)
		require.Equal(t, "https://app.example", configured,
			"%q pins the same origin as every other spelling of it", spelling)

		for _, candidate := range []string{
			"https://app.example", "https://APP.example", "https://app.example:443",
		} {
			ctx, err := WithVerifiedPublicOrigin(t.Context(), candidate)
			require.NoError(t, err, "%q is the pinned origin spelled %q", candidate, spelling)
			verified, ok := VerifiedPublicOrigin(ctx)
			require.True(t, ok)
			require.Equal(t, "https://app.example", verified)
		}

		_, err := WithVerifiedPublicOrigin(t.Context(), "https://other.example")
		require.ErrorIs(t, err, ErrPublicOriginNotConfigured,
			"a different origin is still refused when %q is pinned", spelling)
	}
}

// R1019-N12/f2: the setter must refuse a value it cannot canonicalize rather than
// store it. Storing the raw string kept the process running with a configured origin
// that could never equal any canonicalized candidate, so every comparison failed for
// the life of the process and nothing said why.
func TestR1019ConfiguringAnUnusableOriginIsRefused(t *testing.T) {
	t.Cleanup(func() { _ = SetConfiguredPublicOrigin("") })
	require.NoError(t, SetConfiguredPublicOrigin("https://app.example"))

	for _, unusable := range refusedPublicOriginVectors {
		if unusable == "" {
			continue // the empty value means "none pinned", which is a local runtime
		}
		require.Error(t, SetConfiguredPublicOrigin(unusable),
			"%q cannot be canonicalized, so pinning it would make every comparison fail", unusable)
	}

	// And the pin is unchanged by a refused call, so a failed reconfiguration cannot
	// leave the process comparing against something it never accepted.
	configured, ok := ConfiguredPublicOrigin()
	require.True(t, ok)
	require.Equal(t, "https://app.example", configured)
}
