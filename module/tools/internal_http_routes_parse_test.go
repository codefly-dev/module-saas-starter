package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

// The scanner is the half of this gate that reads TypeScript, and its failure
// modes are silent by construction: a handler whose token check it cannot see
// reads exactly like a handler that never gated anything, which is the drift
// the gate exists to catch. These pin what it must see and what it must refuse
// to guess.

func TestScanRouteModuleAttributesTheGateToTheExportedHandler(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

export async function GET(request: Request): Promise<Response> {
	if (!isTrustedInternalCall(request)) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	return Response.json({ ok: true });
}

export async function POST(): Promise<Response> {
	return Response.json({ ok: true });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "GET")
}

// A route module may serve several methods from one function. Attributing the
// gate to the function alone would declare no method at all.
func TestScanRouteModuleAttributesAliasedExportsToEveryMethod(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

async function handler(request: Request) {
	if (!isTrustedInternalCall(request)) {
		return new Response(null, { status: 401 });
	}
	return new Response(null, { status: 204 });
}

export { handler as DELETE, handler as POST };
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "DELETE", "POST")
}

// Prose about the gate is not a call to it, and neither is its name in a
// string. Either one would declare a route the mesh then denies for nothing.
func TestScanRouteModuleIgnoresTheGateInCommentsAndStrings(t *testing.T) {
	scan := scanRouteModule("route.ts", `
/**
 * This route is public; isTrustedInternalCall gates the sibling one.
 */
export async function GET(): Promise<Response> {
	// isTrustedInternalCall is deliberately not called here.
	return Response.json({ gate: "isTrustedInternalCall" });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan)
}

// Verifying the caller through a shared helper is ordinary code — registration
// does exactly that for both of its writing methods — so the check follows the
// module's own call graph rather than demanding the call sit in the handler.
func TestScanRouteModuleFollowsAModuleScopeHelper(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

function trusted(request: Request): boolean {
	return isTrustedInternalCall(request);
}

export async function POST(request: Request): Promise<Response> {
	if (!trusted(request)) {
		return new Response(null, { status: 401 });
	}
	return new Response(null, { status: 204 });
}

export async function GET(): Promise<Response> {
	return Response.json({ public: true });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "POST")
}

// The call graph is followed only inside the module, so a check performed
// outside every top-level function belongs to no handler and is refused.
func TestScanRouteModuleRefusesAGateOutsideEveryFunction(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

const trusted = isTrustedInternalCall(new Request("https://example.test"));

export async function POST(): Promise<Response> {
	return new Response(null, { status: trusted ? 204 : 401 });
}
`)
	assertGated(t, scan)
	assertProblem(t, scan, "outside any top-level function")
}

// The route a gate protects is read off the direct call, so an alias inside a
// handler would gate a method this check attributes to nothing.
func TestScanRouteModuleRefusesAnAliasedGate(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

export async function POST(request: Request): Promise<Response> {
	const trusted = isTrustedInternalCall;
	if (!trusted(request)) {
		return new Response(null, { status: 401 });
	}
	return new Response(null, { status: 204 });
}
`)
	assertGated(t, scan)
	assertProblem(t, scan, "bound to another name rather than called")
}

// A route is internal because it verifies a credential, not because it verifies
// one particular credential. Registration moved from the shared cluster-internal
// token to a signed, solution-bound one; keyed to a single function name, the
// check read that route as no longer internal while the binding still declared
// it and the mesh still denied it.
func TestScanRouteModuleRecognisesEveryCredentialCheck(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import {
	consumeRegistrationToken,
	SOLUTION_REGISTRATION_HEADER,
	verifySolutionRegistration,
} from "@/solutions/registration-authority";

async function authorize(request: Request) {
	const claims = await verifySolutionRegistration(
		request.headers.get(SOLUTION_REGISTRATION_HEADER),
	);
	if (!claims) {
		return null;
	}
	return consumeRegistrationToken(claims) ? claims : null;
}

export async function POST(request: Request): Promise<Response> {
	if (!(await authorize(request))) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	return Response.json({ ok: true });
}

export async function DELETE(request: Request): Promise<Response> {
	if (!(await authorize(request))) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	return Response.json({ ok: true });
}

export async function GET(): Promise<Response> {
	return Response.json({ solutions: [] });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "DELETE", "POST")
}

// An import clause may rename the gate. Matching only the exported name read an
// aliased gate as absent, and an absent gate is a route nothing requires to be
// declared — the fail-open this whole check exists to remove, inside the check.
func TestScanRouteModuleResolvesAnAliasedImport(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall as gate } from "@/lib/internal-token";

export async function POST(request: Request): Promise<Response> {
	if (!gate(request)) {
		return new Response(null, { status: 401 });
	}
	return new Response(null, { status: 204 });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "POST")
}

// Binding the gate to another name at module scope hides the call from the
// handler it belongs to, so the binding itself is refused.
func TestScanRouteModuleRefusesAModuleScopeBinding(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

const gate = isTrustedInternalCall;

export async function POST(request: Request): Promise<Response> {
	return gate(request) ? new Response(null, { status: 204 }) : new Response(null, { status: 401 });
}
`)
	assertGated(t, scan)
	assertProblem(t, scan, "bound to another name rather than called")
}

// `return /["']/` ends in a letter. Reading only the preceding punctuation made
// the slash a division, and the quote inside the character class then opened a
// literal that blanked through the token check further down the module.
func TestScanRouteModuleReadsARegularExpressionAfterAKeyword(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

function suspicious(id: string): boolean {
	return /["']/.test(id);
}

export async function POST(request: Request): Promise<Response> {
	if (suspicious("x")) {
		return new Response(null, { status: 400 });
	}
	if (!isTrustedInternalCall(request)) {
		return new Response(null, { status: 401 });
	}
	return new Response(null, { status: 204 });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "POST")
}

// The other direction of the same judgement: a slash after a value divides.
// Treating it as a regular expression would blank forward to the next slash and
// swallow whatever the handler does in between.
func TestScanRouteModuleReadsDivisionAfterAValue(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

export async function POST(request: Request): Promise<Response> {
	const budget = Number(request.headers.get("x-budget") ?? "0");
	const share = budget / 2;
	const ratio = "10" / 5;
	if (!isTrustedInternalCall(request)) {
		return new Response(null, { status: 401 });
	}
	return Response.json({ share, ratio });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "POST")
}

// A lexer mistake nobody anticipated must not read as "this module gates
// nothing". Unbalanced delimiters are what a blanking mistake leaves behind.
func TestScanRouteModuleRefusesAModuleItCannotRead(t *testing.T) {
	scan := scanRouteModule("route.ts", `
export async function GET(): Promise<Response> {
	return Response.json({ ok: true });
`)
	assertGated(t, scan)
	assertProblem(t, scan, "cannot be read reliably")
}

// Only a module-scope declaration can back an aliased export. A function nested
// inside another handler that happens to share its name shadowed it, and the
// real handler's token check was then reported as sitting outside any handler.
func TestScanRouteModuleIgnoresANestedFunctionOfTheSameName(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

async function handler(request: Request) {
	if (!isTrustedInternalCall(request)) {
		return new Response(null, { status: 401 });
	}
	return new Response(null, { status: 204 });
}

export function GET() {
	function handler() {
		return 1;
	}
	return new Response(String(handler()));
}

export { handler as POST };
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "POST")
}

// A destructured parameter opens a brace before the body does; matching it as
// the body ends the handler's span at the parameter list and loses the gate.
func TestScanRouteModuleReadsPastADestructuredParameter(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

export async function PUT(request: Request, { params }: RouteContext) {
	if (!isTrustedInternalCall(request)) {
		return new Response(null, { status: 401 });
	}
	return new Response(null, { status: 204 });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "PUT")
}

func TestScanRouteModuleReadsAnExemptionMarker(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

// codefly:internal-http-route-exempt GET: read over pod-local loopback, which
// the mesh never captures.
export async function GET(request: Request): Promise<Response> {
	if (!isTrustedInternalCall(request)) {
		return new Response(null, { status: 401 });
	}
	return Response.json({ ok: true });
}
`)
	assertNoProblems(t, scan)
	assertGated(t, scan, "GET")
	if !strings.HasPrefix(scan.exemptions["GET"], "read over pod-local loopback") {
		t.Errorf("exemption reason = %q", scan.exemptions["GET"])
	}
}

// An exemption is a claim about a specific gated handler. Accepting one that
// names no reason, or one on a handler that gates nothing, would reintroduce
// exactly the silence it exists to replace.
func TestScanRouteModuleRejectsAnEmptyExemption(t *testing.T) {
	scan := scanRouteModule("route.ts", `
import { isTrustedInternalCall } from "@/lib/internal-token";

// codefly:internal-http-route-exempt GET:
export async function GET(request: Request): Promise<Response> {
	return isTrustedInternalCall(request)
		? Response.json({ ok: true })
		: new Response(null, { status: 401 });
}
`)
	assertProblem(t, scan, "states no reason")
	if _, exempt := scan.exemptions["GET"]; exempt {
		t.Error("an exemption with no reason was accepted")
	}
}

func TestScanRouteModuleRejectsAnExemptionForAnUngatedHandler(t *testing.T) {
	scan := scanRouteModule("route.ts", `
// codefly:internal-http-route-exempt POST: nothing gates this.
export async function POST(): Promise<Response> {
	return Response.json({ ok: true });
}
`)
	assertProblem(t, scan, "exempts a handler that does not check")
	if _, exempt := scan.exemptions["POST"]; exempt {
		t.Error("an exemption for an ungated handler was accepted")
	}
}

// module.codefly.yaml carries no integrity protection, so narrowing this gate by
// reading it unchecked would make it a self-service waiver: dropping one line
// from `services:` would take the frontend's internal routes out of scope with
// no error and green CI. The narrowing is the bounded, disclosed one the
// production walkers share (#600), and this pins the property that matters
// here — a service declaring internal routes can never be skipped.
func TestNoServiceWithInternalRoutesCanBeComposedOut(t *testing.T) {
	moduleDir := findModuleDir(t)
	nonComposed := nonComposedServiceDirectories(t, []string{moduleDir})

	declaring := 0
	for _, service := range loadTopologyBindings(t, moduleDir).Services {
		if len(service.InternalHTTPRoutes) == 0 {
			continue
		}
		declaring++
		if nonComposed[filepath.Join(moduleDir, "services", service.Name)] {
			t.Errorf("service %q declares internal_http_routes but is out of this gate's scope; the correspondence would go unchecked", service.Name)
		}
	}
	if declaring == 0 {
		t.Fatal("no service declares internal_http_routes; this gate would assert nothing")
	}
}

// The compiler's accepted method set and Next's handler set are different sets,
// and the difference is load-bearing: Next serves HEAD and OPTIONS, the binding
// cannot name them, and restating either set here would demand a declaration
// the compiler rejects. Read the compiler's set from its source and pin the
// relationship between the two.
func TestDeclarableMethodsComeFromTheTopologyCompiler(t *testing.T) {
	declarable := declarableInternalHTTPMethods(t, findModuleDir(t))

	for _, method := range []string{"DELETE", "GET", "PATCH", "POST", "PUT"} {
		if !declarable[method] {
			t.Errorf("%s admits %s, which %s does not list", internalHTTPMethodSource, method, internalHTTPMethodMap)
		}
	}
	for _, method := range []string{"HEAD", "OPTIONS"} {
		if declarable[method] {
			t.Errorf("%s now admits %s; the gate no longer needs to report it as undeclarable", internalHTTPMethodSource, method)
		}
	}
	// The dangerous direction: a method the compiler accepts but Next never
	// serves could be declared and would never resolve to a handler, which is
	// the "policy matching nothing" this gate reports.
	for _, method := range sortedSet(declarable) {
		if !nextRouteHandlerMethods[method] {
			t.Errorf("%s admits %s, which Next.js does not serve from a route module", internalHTTPMethodSource, method)
		}
	}
}

// A gated HEAD or OPTIONS handler is a conflict between two artifacts, not a
// missing declaration. Treating it as the latter demanded a binding entry the
// topology compiler rejects, leaving the author no way to make both gates pass.
func TestPartitionGatedMethodsSeparatesWhatCannotBeDeclared(t *testing.T) {
	declarable := map[string]bool{"DELETE": true, "GET": true, "PATCH": true, "POST": true, "PUT": true}
	gated := map[string]bool{"HEAD": true, "OPTIONS": true, "POST": true}

	declared, undeclarable := partitionGatedMethods(gated, declarable)
	if strings.Join(declared, ",") != "POST" {
		t.Errorf("declarable = %v, want [POST]", declared)
	}
	if strings.Join(undeclarable, ",") != "HEAD,OPTIONS" {
		t.Errorf("undeclarable = %v, want [HEAD OPTIONS]", undeclarable)
	}
}

// The mesh policy matches a literal path, so a gated handler under a dynamic
// segment has no declarable pair — the reason the derivation reports it rather
// than rendering a path that can never match.
func TestRouteModulePathFollowsNextSegmentRules(t *testing.T) {
	for _, testCase := range []struct {
		file    string
		path    string
		dynamic bool
	}{
		{file: "/app/api/internal/solutions/route.ts", path: "/api/internal/solutions"},
		{file: "/app/(dashboard)/api/health/route.ts", path: "/api/health"},
		{file: "/app/route.ts", path: "/"},
		{file: "/app/api/solutions/[id]/proxy/route.ts", path: "/api/solutions/[id]/proxy", dynamic: true},
	} {
		path, dynamic, err := routeModulePath("/app", testCase.file)
		if err != nil {
			t.Fatalf("routeModulePath(%q): %v", testCase.file, err)
		}
		if path != testCase.path || dynamic != testCase.dynamic {
			t.Errorf("routeModulePath(%q) = %q, %v; want %q, %v", testCase.file, path, dynamic, testCase.path, testCase.dynamic)
		}
	}
}

func assertGated(t *testing.T, scan routeModuleScan, methods ...string) {
	t.Helper()
	got := strings.Join(sortedKeys(scan.gated), ",")
	want := strings.Join(methods, ",")
	if got != want {
		t.Errorf("gated methods = %q, want %q", got, want)
	}
}

func assertNoProblems(t *testing.T, scan routeModuleScan) {
	t.Helper()
	for _, problem := range scan.problems {
		t.Errorf("unexpected problem: %s", problem)
	}
}

func assertProblem(t *testing.T, scan routeModuleScan, substring string) {
	t.Helper()
	for _, problem := range scan.problems {
		if strings.Contains(problem, substring) {
			return
		}
	}
	t.Errorf("no problem mentioning %q; got %v", substring, scan.problems)
}
