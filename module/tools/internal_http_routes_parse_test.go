package tools

import (
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

// A gate reached through a helper cannot be attributed to a method, so the
// route would silently stay out of internal_http_routes. Refuse rather than
// guess.
func TestScanRouteModuleRefusesAnUnattributableGate(t *testing.T) {
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
`)
	assertGated(t, scan)
	assertProblem(t, scan, "outside an exported route handler")
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
	assertProblem(t, scan, "referenced without being called")
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
		path, dynamic := routeModulePath("/app", testCase.file)
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
