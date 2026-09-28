package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The registration/installation boundary is a DECISION, not an accident, and it
// MOVED — partly — with issue #949.
//
// What it was: a deployment-wide registration governed UI and API availability,
// while a per-org installation governed only the solution agent's authority, so
// nothing about a tenant affected what any surface served.
//
// What it is now: the PROJECTIONS — the navigation menu and the per-client surface
// listing — answer per organization and per viewer, narrowed through installations
// and the viewer's scope grants. What did NOT move is route and page exposure:
// `/s/{id}` still renders and `/solutions/{id}/…` still proxies for any caller the
// gateway authenticates, whatever any organization installed. Uninstalling still
// takes no page or route away.
//
// So this file now guards two things rather than one, and the halves pull in
// opposite directions. The surfaces that must STAY installation-blind are scanned
// for coupling as before; the projections that must now BE installation-aware are
// asserted to consult it, so the narrowing cannot silently regress to the
// deployment-wide answer it replaced. module/SOLUTION_REGISTRATION.md §4 states
// both halves, and the claim test below keeps the doc and the code together.

// registrationSurfaces are the files that decide whether a solution's UI and API
// are SERVED — route and page exposure, which remains deployment-wide. None of
// them may consult installation, entitlement, or tenant state; if one ever does,
// that half of the boundary has moved too and the doc has to move with it.
//
// registry.ts is here because its findSolution is what `/s/{id}` renders from.
// The per-viewer narrowing lives in projections.ts precisely so this file can
// stay in the scan; the projection routes are covered by
// TestSolutionProjectionsNarrowByInstallation below, which requires the opposite.
var registrationSurfaces = []string{
	"services/frontend/code/src/solutions/registry.ts",
	"services/frontend/code/src/app/(dashboard)/s/[solutionId]/page.tsx",
	"services/auth-gateway/code/gateway_solutions.go",
}

// projectionSurfaces are the files that decide what a VIEWER is shown. Each must
// consult the per-viewer entitlement read, because a projection that does not is
// the deployment-wide listing this issue removed — and that regression would look
// like working code, since every viewer would simply see everything.
var projectionSurfaces = []string{
	"services/frontend/code/src/app/api/solutions/register/route.ts",
	"services/frontend/code/src/app/api/solutions/surfaces/route.ts",
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
		// The half that moved with #949. Without these the doc would still read as
		// though every caller saw every registered solution.
		"the **projections** answer per organization and per viewer",
		"`installations.solution_identifier` is the registered manifest `id`",
	} {
		if !strings.Contains(document, strings.Join(strings.Fields(claim), " ")) {
			t.Errorf("SOLUTION_REGISTRATION.md no longer states %q", claim)
		}
	}
}

// TestSolutionProjectionsNarrowByInstallation is the inverse of the scan above:
// these files MUST consult the per-viewer entitlement read.
//
// It exists because the regression it catches is invisible. A projection that
// stopped narrowing would not throw, would not fail a type check and would return
// a perfectly well-formed list — just the deployment-wide one, to every viewer.
// Asserting the coupling is present is the only way that shows up as a failure.
func TestSolutionProjectionsNarrowByInstallation(t *testing.T) {
	moduleDir := findModuleDir(t)
	for _, relative := range projectionSurfaces {
		data, err := os.ReadFile(filepath.Join(moduleDir, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		code := codeOnly(string(data))
		// The entitlement read, and the narrowing that consumes it. This is a
		// presence check and proves only that: a route that imported both and then
		// projected the registered set anyway would still pass here. That behaviour
		// is caught by the route tests (a deployed but uninstalled solution must be
		// absent from each projection); this test catches the coarser regression of
		// a projection route dropping the narrowing entirely.
		for _, identifier := range []string{
			"viewerEntitlements",
			"entitledSolutions",
		} {
			if !strings.Contains(code, identifier) {
				t.Errorf(
					"%s does not reference %q: a solution projection must narrow through installations and the viewer's grants (SOLUTION_REGISTRATION.md §4, issue #949), never answer the deployment-wide set",
					relative, identifier,
				)
			}
		}
	}
}

// TestSolutionProjectionsDoNotDeriveIdentityLocally keeps the projections off the
// one shortcut that would make the narrowing worthless.
//
// `lib/auth-session.ts` can read an organization out of an access token, but
// `decodeJWTPayload` only base64-decodes it — it verifies nothing. A projection
// that narrowed on that would let any caller read another tenant's menu by editing
// one claim, and it would pass every other test in this file: it consults
// installation state, it narrows, and it is wrong.
func TestSolutionProjectionsDoNotDeriveIdentityLocally(t *testing.T) {
	moduleDir := findModuleDir(t)
	surfaces := append([]string{
		"services/frontend/code/src/solutions/entitlements.ts",
		"services/frontend/code/src/solutions/projections.ts",
		"services/frontend/code/src/solutions/registry.ts",
	}, projectionSurfaces...)
	for _, relative := range surfaces {
		data, err := os.ReadFile(filepath.Join(moduleDir, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		code := codeOnly(string(data))
		for _, forbidden := range []string{
			"extractSessionContext",
			"decodeJWTPayload",
			"extractRoles",
		} {
			if strings.Contains(code, forbidden) {
				t.Errorf(
					"%s references %q: it decodes an access token without verifying it, so an organization read from it is one the CALLER chose. The verified tenant and viewer come from the gateway's ext_authz stamp (services/frontend/code/src/solutions/entitlements.ts)",
					relative, forbidden,
				)
			}
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
