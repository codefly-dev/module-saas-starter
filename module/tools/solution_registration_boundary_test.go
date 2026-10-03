package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The registration/installation boundary is a DECISION, not an accident, and it
// has MOVED TWICE.
//
// What it was: a deployment-wide registration governed UI and API availability,
// while a per-org installation governed only the solution agent's authority, so
// nothing about a tenant affected what any surface served.
//
// #949 moved the PROJECTIONS — the navigation menu and the per-client surface
// listing answer per organization and per viewer, narrowed through installations
// and the viewer's scope grants. It deliberately left route and page exposure
// deployment-wide.
//
// #952 moves the PROXY, because leaving it was a hole rather than a boundary: a
// viewer in any organization could call the data endpoints of every solution the
// deployment ran — with a real bearer forwarded to them — by typing the path the
// menu declined to show. Available, installed and exposed are three layers and
// none is inferred from another, so the component that holds the verified
// identity and forwards the credential is where the second and third are
// enforced for traffic.
//
// What has NOT moved, and why each is deliberate rather than pending:
//
//   - The PUBLIC Module-Federation surface (`/solutions/{id}/assets/*` and
//     `/.well-known/*`) is fetched by the browser's module loader with no
//     credential, so there is no viewer to ask about. It serves the solution's
//     own static bytes, which carry no tenant data.
//   - The `/s/{id}` PAGE server-renders from the registry. The access token
//     lives in this origin's memory, not in a cookie the server can read, so a
//     server-side admission check would have to exchange the httpOnly refresh
//     cookie — rotating a viewer's refresh token on every page render. The page
//     discloses a nav title and a manifest URL the public asset surface already
//     serves; every call it makes goes through the gated proxy.
//
// So this file guards two things, and the halves pull in opposite directions.
// The surfaces that must STAY installation-blind are scanned for coupling; the
// surfaces that must now BE installation-aware are asserted to consult it, so
// the narrowing cannot silently regress to the deployment-wide answer it
// replaced. module/SOLUTION_REGISTRATION.md §4 states every half, and the claim
// test below keeps the doc and the code together.

// registrationSurfaces are the files that answer from the deployment-wide
// registry alone. None of them may consult installation, entitlement, or tenant
// state; if one ever does, that half of the boundary has moved too and the doc
// has to move with it.
//
// registry.ts is here because its findSolution is what `/s/{id}` renders from,
// and the page is here for the reason stated at the top of this file: this
// origin cannot read a verified viewer server-side without rotating a refresh
// token per render. The per-viewer narrowing lives in projections.ts precisely
// so registry.ts can stay in this scan.
//
// gateway_solutions.go LEFT this list with #952. It is now asserted in
// TestSolutionTrafficIsAdmittedByInstallation below, which requires the
// opposite.
var registrationSurfaces = []string{
	"services/frontend/code/src/solutions/registry.ts",
	"services/frontend/code/src/app/(dashboard)/s/[solutionId]/page.tsx",
}

// projectionSurfaces are the files that decide what a VIEWER is shown. Each must
// consult the per-viewer entitlement read, because a projection that does not is
// the deployment-wide listing #949 removed — and that regression would look
// like working code, since every viewer would simply see everything.
var projectionSurfaces = []string{
	"services/frontend/code/src/app/api/solutions/register/route.ts",
	"services/frontend/code/src/app/api/solutions/surfaces/route.ts",
}

// trafficSurfaces are the files that decide whether a REQUEST reaches a
// solution. The proxy must consult the per-viewer admission before it forwards
// anything, and the regression it guards against is the one #952 fixed: a proxy
// that routes on registration alone serves every solution to every
// organization, and looks like working code while doing it.
var trafficSurfaces = []string{
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
		"Other tenants are unaffected",
		// The half that moved with #949. Without these the doc would still read as
		// though every caller saw every registered solution.
		"the **projections** answer per organization and per viewer",
		// The join key, pinned because it is the one the §9 consent-transfer
		// attack turned on: an alias is reusable, an identity is not. A change
		// that moves this line back to the alias is the regression.
		"`installations.target_id` is the target a",
		"**never reused**",
		"Closing a target revokes every active installation of it, in the same",
		"Presence nothing declared is admissible to nobody.",
		// The half that moved with #952. The first sentence is the one a reader
		// needs in order to know that registration alone no longer reaches a
		// solution; the second and third are the two answers that must not be
		// collapsed into one, which is the mistake this gate is most likely to
		// be "simplified" into.
		"the solution proxy admits traffic through the same authority the",
		"An outage is not a verdict.",
		"Health is not admission.",
		// The residual exposure, stated as a decision rather than left to be
		// rediscovered as a gap.
		"The public Module-Federation surface is not gated, and cannot be.",
		"does **not** remove the solution's page for that organization",
		// §7b. The host's own control paths, published because three
		// repositories independently invented `/platform/_credential` from a
		// design note and nothing failed at build time. The paths are pinned
		// here so a consumer reading this document is reading a contract: a
		// rename that does not move these lines is a rename that breaks every
		// consumer silently.
		"`POST /platform/_credential`",
		"`POST /platform/_delivery/presence`",
		"`POST /platform/_delivery/authority`",
		// The rule that makes hardcoding the path correct and hardcoding the
		// origin wrong. Without it the table reads as an invitation to pin
		// everything.
		"a path is a contract, an origin is a resolution result",
		"`/platform/` is **reserved for the host**",
		// The trust model, pinned because each of these is a claim a reader
		// would otherwise have to take on faith from a code comment.
		//
		// The carrier shape and the signing encoding: a consumer that signs the
		// YAML this repository's Marshal writes produces a payload the host
		// refuses even with a genuine attestation over it, and the document is
		// the only place that is stated.
		"The signing input is the canonical JSON",
		// The delivery carrier contract, pinned because every line of it was
		// something one of the three implementing repositories had wrong, and a
		// rename that does not move these lines breaks a consumer silently.
		//
		// The authority pair is the only hardcoded (SA, namespace) in the
		// scheme: a wrong constant there makes the TokenReview SUCCEED on a
		// genuine identity and the host refuse the real carrier for a name.
		"| authority | `delivery` | `platform-authority` — a fixed pair |",
		// The presence namespace is NOT a constant, and the reason is the half
		// that gets lost first: a fixed value refuses every genuine carrier.
		"**the namespace the document's own workloads declare**",
		// Offline verification, and the failure shape that makes it matter.
		"would **hang rather than fail fast**",
		// That the host is the second layer, not the perimeter. Without this
		// line the check above reads as the only thing standing in the way.
		"The host's SA-and-namespace check is the second",
		// The named refusal. The library collapses these two; the distinction
		// is the difference between "fix your signer" and "investigate".
		"refused BY NAME",
		// WHERE the check goes, which is the load-bearing half. The obvious
		// implementation — classify the verifier's error — cannot work: the
		// verifier emits the same words for "the signer never logged" and "the
		// root cannot check the log it did".
		"Check it BEFORE the verifier runs",
		// And that a signed timestamp is not evidence of logging. This is the
		// case a looser "has it got anything?" check admits silently.
		"does not stand in for transparency evidence",
		// The delivery response taxonomy, pinned because a Job's retry policy is
		// written against it. The 401/503 split especially: an earlier draft
		// grouped on HTTP class and made a reviewable refusal retryable.
		"what a retry would change",
		"The review ran and refused",
		// That the local policy is coordinate-bound. Without this line the
		// table reads as "choose either", and the one that performs no
		// cryptography is the convenient choice.
		"a **local coordinate only**",
		// And that the production policy is not available yet. A consumer
		// planning against this document must not read `keyless` as live.
		"**NOT YET AVAILABLE**",
		// That a bundle is required and must be an object. A renderer whose
		// pipeline has no signing step yet reaches for the field it can fill —
		// `null` — and nothing in the carrier's own shape would have told it
		// that produces a document the host treats as unsigned.
		"A bundle must be present and be a JSON object",
		// That what lands in a row is derived from the attested bytes rather
		// than from whatever the pass was holding. This is the claim that makes
		// the stored digest meaningful, and it is a property of the dependency,
		// so a reader cannot check it by reading this repository alone.
		"What the host stores is derived from the attested bytes",
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

// TestSolutionTrafficIsAdmittedByInstallation is the #952 half of the inverse
// scan: the solution proxy MUST consult the per-viewer admission.
//
// The regression is invisible for the same reason the projection one is. A proxy
// that dropped the check throws nothing, logs nothing and answers 200 — it just
// serves every registered solution to every organization, which is precisely the
// behaviour that was there before and the reason this hole existed for as long as
// it did. Asserting the coupling is present is what makes its removal a failure.
//
// Presence is all this proves. That an UNINSTALLED solution is actually refused,
// that an authority outage is a 503 rather than a verdict, and that the public
// asset surface stays ungated are proven by the gateway's own tests
// (gateway_solution_admission_test.go), each of which answers 200 against the
// pre-#952 gateway.
func TestSolutionTrafficIsAdmittedByInstallation(t *testing.T) {
	moduleDir := findModuleDir(t)
	for _, relative := range trafficSurfaces {
		data, err := os.ReadFile(filepath.Join(moduleDir, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		code := codeOnly(string(data))
		for _, identifier := range []string{
			// The authority call, and the refusal that is a verdict rather than
			// an outage. Both, because a proxy that called the authority and
			// then forwarded regardless of the answer would pass on the first
			// alone.
			"admitViewerSolution",
			"viewerSolutionNotEntitled",
			"viewerSolutionUndecidable",
		} {
			if !strings.Contains(code, identifier) {
				t.Errorf(
					"%s does not reference %q: solution traffic must be admitted through the viewer's installation and grants (SOLUTION_REGISTRATION.md §4, issue #952), never routed on registration alone",
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
