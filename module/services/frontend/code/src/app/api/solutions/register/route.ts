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
 * execution) or nav entry. Fails closed when the secret is unset.
 *
 * That token is the ONLY gate, and a NetworkPolicy cannot add a second one:
 * `frontend/http` is a public module export, so this path shares TCP 3000 with
 * every browser-facing page, and a NetworkPolicy selects pods and ports, never
 * paths. In the generated base topology the only ingress rule for the frontend
 * admits the Istio ingress gateway, so a solution registers through the public
 * front door rather than from inside the mesh. See
 * module/DEPLOYMENT_TOPOLOGY.md, "HTTP internal surfaces are not mesh-gated".
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
	// A solution whose registration was deregistered has to say so to come back:
	// an ordinary retry from a retiring deployment must not resurrect what an
	// operator removed.
	const reactivate =
		typeof body === "object" &&
		body !== null &&
		(body as { reactivate?: unknown }).reactivate === true;
	const result = await registerSolution(manifest, { reactivate });
	if (!result.ok) {
		return writeFailure(result.reason);
	}
	return Response.json({
		ok: true,
		id: manifest.id,
		revision: result.revision,
		status: result.status,
	});
}

/**
 * Registration is a write to a shared, versioned record, so it can fail in ways
 * a caller must tell apart: `conflict` means re-read and retry, `forbidden`
 * means the id belongs to someone else, `unavailable` means back off. Answering
 * 200 for any of these would let a solution believe it is serving when it is
 * not — the exact incoherence this registry exists to prevent.
 */
function writeFailure(
	reason: "unavailable" | "conflict" | "forbidden",
): Response {
	switch (reason) {
		case "conflict":
			return Response.json({ error: "revision_conflict" }, { status: 409 });
		case "forbidden":
			return Response.json(
				{ error: "not_registration_owner" },
				{ status: 403 },
			);
		default:
			return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
}

export async function DELETE(request: Request): Promise<Response> {
	if (!isTrustedInternalCall(request)) {
		return Response.json({ error: "unauthorized" }, { status: 401 });
	}
	const id = new URL(request.url).searchParams.get("id");
	if (!id) {
		return Response.json({ error: "missing_id" }, { status: 400 });
	}
	const result = await unregisterSolution(id);
	if (!result.ok) {
		return writeFailure(result.reason);
	}
	return Response.json({ ok: true, revision: result.revision });
}

// GET is the public navigation projection: the id and the nav entry the browser
// polls to render the Solutions menu, and nothing else. It is deliberately not
// gated on the internal token — every signed-in browser needs it — so it must
// carry no field a browser does not render. Where a solution's code is served
// from, which backend service fronts it, and its dashboard declaration are
// deployment topology; they are served by the internal detail lookup
// (app/api/internal/solutions) to callers holding the cluster-internal token.
export async function GET(): Promise<Response> {
	const registered = await loadSolutions();
	// An empty registry and an unreadable one must not render the same: the
	// first correctly shows no solutions, the second would silently empty a
	// working navigation.
	if (registered === "unavailable") {
		return Response.json({ error: "registry_unavailable" }, { status: 503 });
	}
	return Response.json({ solutions: registered.map(navProjection) });
}
