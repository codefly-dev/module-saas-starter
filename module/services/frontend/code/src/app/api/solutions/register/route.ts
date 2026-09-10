import { isTrustedInternalCall } from "@/lib/internal-token";
import {
	loadSolutions,
	navProjection,
	parseManifest,
	registerSolution,
	unregisterSolution,
} from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

/**
 * Self-registration endpoint. A solution POSTs its manifest here on startup so
 * the host learns about it at runtime. This is generic: the host validates the
 * shape and stores it, never referencing any specific solution.
 *
 * Registration mutates what the nav renders and what the solution route loads
 * as a Module Federation remote, so it is NOT public: it requires the
 * cluster-internal token. Without this, any caller that can reach the frontend
 * could register an attacker-controlled MF remote (arbitrary in-origin script
 * execution) or nav entry. Fails closed when the secret is unset; in a deployed
 * environment this should additionally be reachable solely from inside the mesh
 * (NetworkPolicy).
 */
export async function POST(request: Request): Promise<Response> {
	if (!isTrustedInternalCall(request)) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	let body: unknown;
	try {
		body = await request.json();
	} catch {
		return Response.json({ error: "invalid_json" }, { status: 400 });
	}
	const manifest = parseManifest(body);
	if (!manifest) {
		return Response.json({ error: "invalid_manifest" }, { status: 422 });
	}
	registerSolution(manifest);
	return Response.json({ ok: true, id: manifest.id });
}

export async function DELETE(request: Request): Promise<Response> {
	if (!isTrustedInternalCall(request)) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	const id = new URL(request.url).searchParams.get("id");
	if (!id) {
		return Response.json({ error: "missing_id" }, { status: 400 });
	}
	unregisterSolution(id);
	return Response.json({ ok: true });
}

// GET is the public navigation projection: the id and the nav entry the browser
// polls to render the Solutions menu, and nothing else. It is deliberately not
// gated on the internal token — every signed-in browser needs it — so it must
// carry no field a browser does not render. Where a solution's code is served
// from, which backend service fronts it, and its dashboard declaration are
// deployment topology; they are served by the internal detail lookup
// (app/api/internal/solutions) to callers holding the cluster-internal token.
export async function GET(): Promise<Response> {
	return Response.json({ solutions: loadSolutions().map(navProjection) });
}
