package business

import "testing"

// Slugify pins the path-segment derivation the team tree builds on: lowercase,
// non-alphanumeric runs collapse to "-", no leading/trailing dashes. The path
// invariants themselves (uniqueness per org, parent-org match, depth cap) are
// enforced by the store schema + CreateTeam and covered by the RLS/DB tests.
func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Platform Eng.":        "platform-eng",
		"engineering":          "engineering",
		"  Data / ML  ":        "data-ml",
		"A--B":                 "a-b",
		"Ops_2026":             "ops-2026",
		"---":                  "",
		"Ünïcode Team":         "n-code-team", // non-ASCII drops (slug charset is [a-z0-9-])
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
