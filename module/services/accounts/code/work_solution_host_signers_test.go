package main

import (
	"strings"
	"testing"
)

// SOLUTION_HOST_SIGNER_DOMAINS is the host's answer to "who may speak for this
// ownership domain", and a mistake in it fails in the quietest possible way: a
// policy that grants nothing reads exactly like a policy that works, until the
// binding it was meant to admit is withheld with a reason about domains.
//
// So every malformed shape is refused BY NAME at boot rather than parsed
// permissively or skipped.

func TestSignerDomainsParsesOneIdentityWithSeveralDomains(t *testing.T) {
	policy, err := parseSolutionHostSignerDomains(
		"https://signer.example/wf@refs/heads/main=acme|beta", []string{"acme", "beta"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	domains := policy["https://signer.example/wf@refs/heads/main"]
	if len(domains) != 2 || domains[0] != "acme" || domains[1] != "beta" {
		t.Fatalf("domains = %v, want [acme beta]", domains)
	}
}

// The separators have to survive a keyless certificate SAN, which contains `/`,
// `:` and `@`. If `=` or `|` had been chosen differently, or `:` used as a
// separator, an identity would split in the middle and the policy would key on
// a prefix.
func TestSignerDomainsKeepsAWorkflowIdentityIntact(t *testing.T) {
	identity := "https://github.example/acme/releases/.github/workflows/publish.yml@refs/tags/v1.2.3"
	policy, err := parseSolutionHostSignerDomains(identity+"=acme", []string{"acme"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := policy[identity]; !ok {
		t.Fatalf("policy keys = %v, want the identity verbatim", keysOf(policy))
	}
}

func TestSignerDomainsParsesSeveralIdentities(t *testing.T) {
	policy, err := parseSolutionHostSignerDomains("a=acme, b=beta", []string{"acme", "beta"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(policy) != 2 || policy["a"][0] != "acme" || policy["b"][0] != "beta" {
		t.Fatalf("policy = %v, want a→acme and b→beta", policy)
	}
}

// A domain the host does not accept at all is the typo that matters: core would
// refuse every document from it, and the reason would point at the domain rather
// than at the policy entry that was wrong.
func TestSignerDomainsRefusesADomainTheHostDoesNotAccept(t *testing.T) {
	_, err := parseSolutionHostSignerDomains("a=acme|typo", []string{"acme"})
	if err == nil {
		t.Fatal("a domain outside SOLUTION_HOST_OWNERSHIP_DOMAINS must be refused")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("error %q does not name the offending domain", err)
	}
}

func TestSignerDomainsRefusesMalformedEntries(t *testing.T) {
	for name, raw := range map[string]string{
		"no separator":     "just-an-identity",
		"no identity":      "=acme",
		"no domain":        "a=",
		"no domain, blank": "a=|",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSolutionHostSignerDomains(raw, []string{"acme"}); err == nil {
				t.Fatalf("%q must be refused", raw)
			}
		})
	}
}

// An empty policy beside a declared mount is the permissive case: core would let
// any signer this host attested deliver under any domain it accepts.
func TestSignerDomainsRefusesAnEmptyPolicy(t *testing.T) {
	for _, raw := range []string{"", "   ", ",", " , "} {
		if _, err := parseSolutionHostSignerDomains(raw, []string{"acme"}); err == nil {
			t.Fatalf("an empty policy (%q) must be refused beside a declared mount", raw)
		}
	}
}

func keysOf(policy map[string][]string) []string {
	out := make([]string, 0, len(policy))
	for key := range policy {
		out = append(out, key)
	}
	return out
}
