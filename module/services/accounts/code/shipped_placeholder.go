package main

import (
	"fmt"
	"strings"
)

// Shipped-placeholder refusal.
//
// The module package ships working local defaults for its secret groups, and the
// repository they live in is public. The configuration-defaults gate requires
// every one of those defaults to carry a placeholder marker, precisely so a real
// credential can never be committed as one. That makes the same marker list the
// answer to the opposite question: a value carrying one is a value everybody
// knows.
//
// Nothing rejected one outside local development, so an unprovisioned secret
// group was not a denied group — it was a live credential with a published value,
// and the perimeter rested on mesh authorization alone.
//
// What is refused here, by name, at boot: the credentials that decide whether a
// caller is inside the perimeter. An optional feature's unprovisioned credential
// is a different question with a different answer — the feature is off — and
// conflating the two would wedge a cell that simply does not use one.

// shippedPlaceholderMarkers are the phrases a shipped default carries. It is the
// same list the configuration-defaults gate requires of every secret default;
// TestShippedPlaceholderMarkersMatchTheShippingGate in module/tools holds the two
// copies in lockstep, because a marker added on one side and not the other means
// either a default that cannot ship or a published value a cell would accept.
//
// Each is a distinctive placeholder phrase. Common English words — "example",
// "dummy" — are deliberately absent: a production-shaped value would borrow one
// as a loose substring and be refused for no reason.
var shippedPlaceholderMarkers = []string{
	"replace-me", "replaceme", "change-me", "changeme",
	"placeholder", "local-dev", "dev-only",
}

// minimumPerimeterCredentialLength is the floor for a credential that decides
// perimeter membership. 32 characters is the shortest value that can carry 128
// bits of entropy in any encoding an operator is likely to generate, and the
// shipped placeholders are shorter — so the length check is not a second spelling
// of the marker check: it also refuses a short hand-typed value that happens to
// carry no marker.
const minimumPerimeterCredentialLength = 32

// looksLikeShippedPlaceholder reports whether value carries a marker.
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
// deployed runtime, or nil. name is the group-qualified key, so the refusal says
// what an operator has to provision rather than that something was wrong.
//
// It never includes the value.
func refusePerimeterCredential(name, value string, current bool) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if current {
			return fmt.Errorf(
				"%s is absent; a deployed cell must provision it before this service can "+
					"decide that a caller is inside the perimeter", name)
		}
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

// perimeterCredential is one value this service trusts to decide that a caller is
// inside the perimeter. Current distinguishes the two cases a single list was
// conflating: a CURRENT credential the deployment must carry, and the PREVIOUS
// half of a rotation, which is absent on every cell that is not mid-rotation.
// Refusing both alike would make the ordinary state an outage; refusing neither
// let a cell start with no perimeter credential at all, which is what R1019-N17
// found. The flag is the distinction, so each answer is given once.
type perimeterCredential struct {
	Name    string
	Value   string
	Current bool
}

// perimeterCredentials are the values this service trusts to decide that a caller
// is inside the perimeter: the cluster-internal token both halves of a rotation,
// and the gateway-provenance token this service believes forwarded identity on.
func perimeterCredentials() []perimeterCredential {
	return []perimeterCredential{
		{"internal-auth/CODEFLY_INTERNAL_TOKEN", workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN"), true},
		{"internal-auth/CODEFLY_INTERNAL_TOKEN_PREVIOUS", workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN_PREVIOUS"), false},
		{"gateway-trust/CODEFLY_GATEWAY_TOKEN", workspaceEnv("gateway-trust", "CODEFLY_GATEWAY_TOKEN"), true},
		{"gateway-trust/CODEFLY_GATEWAY_TOKEN_PREVIOUS", workspaceEnv("gateway-trust", "CODEFLY_GATEWAY_TOKEN_PREVIOUS"), false},
	}
}

// requirePerimeterCredentials refuses to start a deployed runtime holding a
// shipped placeholder, or a perimeter credential too short to be one.
//
// Every refusal is collected rather than the first returned: an operator fixing
// these is editing one secret group, and finding out about the second key on the
// next deploy is a second outage for no reason.
func requirePerimeterCredentials(isLocal bool) error {
	if isLocal {
		return nil
	}
	var problems []string
	for _, credential := range perimeterCredentials() {
		if err := refusePerimeterCredential(credential.Name, credential.Value, credential.Current); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("perimeter credentials are unfit for a deployed runtime: %s",
		strings.Join(problems, "; "))
}
