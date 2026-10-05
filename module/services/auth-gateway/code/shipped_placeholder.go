package main

import (
	"fmt"
	"strings"
)

// Shipped-placeholder refusal — the gateway's half.
//
// The module package ships working local defaults for its secret groups, out of a
// public repository, and the configuration-defaults gate requires each of them to
// carry a placeholder marker so a real credential can never be committed as one.
// That makes the same marker list the answer to the opposite question: a value
// carrying one is a value everybody knows.
//
// This gateway holds two credentials that decide perimeter membership — the
// cluster-internal token it accepts on its own internal endpoints, and the
// gateway-provenance token accounts believes forwarded identity on — and nothing
// rejected a shipped placeholder in either outside local development.
//
// The list and the floor are duplicated from accounts deliberately: the two are
// separate Go modules with no shared local package, and
// module/tools/shipped_placeholder_lockstep_test.go holds the copies identical, so
// a marker added on one side cannot leave the other admitting a published value.

var shippedPlaceholderMarkers = []string{
	"replace-me", "replaceme", "change-me", "changeme",
	"placeholder", "local-dev", "dev-only",
}

// minimumPerimeterCredentialLength is the floor for a credential that decides
// perimeter membership: the shortest value that can carry 128 bits of entropy in
// any encoding an operator is likely to generate. The shipped placeholders are
// shorter, so this is not a second spelling of the marker check — it also refuses
// a short hand-typed value carrying no marker.
const minimumPerimeterCredentialLength = 32

func looksLikeShippedPlaceholder(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range shippedPlaceholderMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// refusePerimeterCredential reports why a perimeter credential is unfit for a
// deployed runtime, or nil. It names the group-qualified key and never the value.
// An empty value is not refused here: whether a credential is required is the
// loader's own question.
func refusePerimeterCredential(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if looksLikeShippedPlaceholder(trimmed) {
		return fmt.Errorf(
			"%s carries a placeholder this module ships in its public local defaults; "+
				"provision a real value for this cell", name)
	}
	if len(trimmed) < minimumPerimeterCredentialLength {
		return fmt.Errorf(
			"%s is %d characters; a credential that decides perimeter membership must be at least %d",
			name, len(trimmed), minimumPerimeterCredentialLength)
	}
	return nil
}

// perimeterCredentials are the values this gateway trusts to decide that a caller
// is inside the perimeter.
func perimeterCredentials() []struct{ Name, Value string } {
	return []struct{ Name, Value string }{
		{"internal-auth/CODEFLY_INTERNAL_TOKEN", workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN")},
		{"internal-auth/CODEFLY_INTERNAL_TOKEN_PREVIOUS", workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN_PREVIOUS")},
		{"gateway-trust/CODEFLY_GATEWAY_TOKEN", workspaceEnv("gateway-trust", "CODEFLY_GATEWAY_TOKEN")},
	}
}

// requirePerimeterCredentials refuses to start a deployed runtime holding a
// shipped placeholder, or a perimeter credential too short to be one. Every
// refusal is collected: an operator fixing these edits one secret group, and
// learning about the second key on the next deploy is a second outage for nothing.
func requirePerimeterCredentials(isLocal bool) error {
	if isLocal {
		return nil
	}
	var problems []string
	for _, credential := range perimeterCredentials() {
		if err := refusePerimeterCredential(credential.Name, credential.Value); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("perimeter credentials are unfit for a deployed runtime: %s",
		strings.Join(problems, "; "))
}
