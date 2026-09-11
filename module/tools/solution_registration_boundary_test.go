package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The registration/installation boundary is a DECISION, not an accident: a
// deployment-wide solution registration governs UI and API availability, while a
// per-org installation governs only the solution agent's authority. Uninstalling
// therefore does not take a page or a gateway route away — from that
// organization or any other.
//
// module/SOLUTION_REGISTRATION.md §4 states that limitation. These tests keep
// the statement and the code from drifting apart in either direction: the doc
// must say it, and the registration surfaces must not quietly grow the coupling
// the doc says they do not have.

// registrationSurfaces are the files that decide whether a solution's UI and API
// are served. None of them may consult installation, entitlement, or tenant
// state — if one ever does, the documented boundary has moved and the doc has to
// move with it.
var registrationSurfaces = []string{
	"services/frontend/code/src/app/api/solutions/register/route.ts",
	"services/frontend/code/src/solutions/registry.ts",
	"services/frontend/code/src/app/(dashboard)/s/[solutionId]/page.tsx",
	"services/auth-gateway/code/gateway_solutions.go",
}

// installationCoupling are the identifiers that would signal a registration
// surface gating on per-tenant admission.
var installationCoupling = []string{
	"InstallSolution",
	"UninstallSolution",
	"installation",
	"Installation",
	"entitlement",
	"Entitlement",
}

var blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// codeOnly strips comments so the coupling scan reads what the file DOES, not
// what it says about itself. Without this, an accurate sentence — "this is
// deliberately not an installation gate" — fails the build, which pressures the
// next reader to delete a true comment to get CI green.
func codeOnly(source string) string {
	source = blockComment.ReplaceAllString(source, "")
	var kept []string
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		// Truncate a trailing comment, but never at the "//" inside a URL scheme.
		for i := 0; i+1 < len(line); i++ {
			if line[i] == '/' && line[i+1] == '/' {
				if i > 0 && line[i-1] == ':' {
					continue
				}
				line = line[:i]
				break
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func TestSolutionRegistrationDoesNotConsultInstallationState(t *testing.T) {
	moduleDir := findModuleDir(t)
	for _, relative := range registrationSurfaces {
		data, err := os.ReadFile(filepath.Join(moduleDir, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		code := codeOnly(string(data))
		for _, identifier := range installationCoupling {
			if strings.Contains(code, identifier) {
				t.Errorf(
					"%s references %q in code: solution registration is documented as independent of per-org installation (SOLUTION_REGISTRATION.md §4) — update the doc if that boundary moved",
					relative, identifier,
				)
			}
		}
	}
}

// TestSolutionRegistrationBoundaryIsDocumented pins the claims that make the
// boundary legible: without them a reader has the code but not the decision.
func TestSolutionRegistrationBoundaryIsDocumented(t *testing.T) {
	moduleDir := findModuleDir(t)
	data, err := os.ReadFile(filepath.Join(moduleDir, "SOLUTION_REGISTRATION.md"))
	if err != nil {
		t.Fatalf("read SOLUTION_REGISTRATION.md: %v", err)
	}
	// Compare with whitespace collapsed: the claim is the sentence, not the
	// column it happens to wrap at, and a reflow must not fail the build.
	document := strings.Join(strings.Fields(string(data)), " ")
	for _, claim := range []string{
		"A registered solution is host-trusted",
		"governs **agent authority only**",
		"does **not** remove the solution's nav entry, page, or gateway route",
		"Other tenants are unaffected",
	} {
		if !strings.Contains(document, strings.Join(strings.Fields(claim), " ")) {
			t.Errorf("SOLUTION_REGISTRATION.md no longer states %q", claim)
		}
	}
}

// TestSolutionAndModuleRegistrationCredentialsAreSeparate keeps the two
// registrant kinds from collapsing into one grant. A solution remote executes in
// the host origin with the viewer's credentials; a module federates a REST
// prefix. Holding one credential must never confer the other, which requires
// both a separate declaration and a separate audience.
func TestSolutionAndModuleRegistrationCredentialsAreSeparate(t *testing.T) {
	moduleDir := findModuleDir(t)

	minter, err := os.ReadFile(filepath.Join(moduleDir, "services/accounts/code/pkg/auth/ed25519/minter.go"))
	if err != nil {
		t.Fatalf("read minter: %v", err)
	}
	for _, audience := range []string{
		`ModuleRegistrationAudience = "module-registration"`,
		`SolutionRegistrationAudience = "solution-registration"`,
	} {
		if !strings.Contains(string(minter), audience) {
			t.Errorf("minter no longer declares %s", audience)
		}
	}

	federation, err := os.ReadFile(filepath.Join(moduleDir, "configurations/local/federation.env"))
	if err != nil {
		t.Fatalf("read federation configuration: %v", err)
	}
	for _, key := range []string{"MODULE_REGISTRATION_SECRETS", "SOLUTION_REGISTRATION_SECRETS"} {
		if !strings.Contains(string(federation), key) {
			t.Errorf("federation configuration no longer declares %s", key)
		}
	}
}
