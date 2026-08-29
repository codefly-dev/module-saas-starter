import { timingSafeEqual } from "node:crypto";

import { getWorkspaceSecret } from "codefly";

import {
	loadSolutions,
	parseManifest,
	registerSolution,
	unregisterSolution,
} from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

const INTERNAL_TOKEN_HEADER = "x-codefly-internal-token";

/**
 * The cluster-internal credential the solution must present to register or
 * unregister. Same secret the frontend uses to prove a trusted origin to the
 * gateway. Returns null when unset so mutations fail closed.
 */
function expectedInternalToken(): string | null {
	const token = getWorkspaceSecret(
		"internal-auth",
		"CODEFLY_INTERNAL_TOKEN",
	)?.trim();
	return token ? token : null;
}

/**
 * Registration mutates what the nav renders and what the solution route loads
 * as a Module Federation remote, so it is NOT public: it requires the
 * cluster-internal token. Without this, any caller that can reach the frontend
 * could register an attacker-controlled MF remote (arbitrary in-origin script
 * execution) or nav entry. Fails closed when the secret is unset.
 */
function isTrustedInternalCall(request: Request): boolean {
	const expected = expectedInternalToken();
	if (!expected) {
		return false;
	}
	const presented = request.headers.get(INTERNAL_TOKEN_HEADER) ?? "";
	const presentedBytes = Buffer.from(presented, "utf8");
	const expectedBytes = Buffer.from(expected, "utf8");
	if (presentedBytes.length !== expectedBytes.length) {
		return false;
	}
	return timingSafeEqual(presentedBytes, expectedBytes);
}

/**
 * Self-registration endpoint. A solution POSTs its manifest here on startup so
 * the host learns about it at runtime. This is generic: the host validates the
 * shape and stores it, never referencing any specific solution.
 *
 * NOTE: cluster-internal only. The POST/DELETE mutations require the internal
 * token above; in a deployed environment this should additionally be reachable
 * solely from inside the mesh (NetworkPolicy).
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

// GET is a read of nav-only metadata (titles/paths) that the browser polls to
// render the Solutions nav. It is intentionally not gated on the internal
// token — it exposes no secrets and no upstreams. The dashboard data graph is
// dropped here: only the solution page reads it (server-side, via findSolution),
// so broadcasting it on every 10s nav poll would ship bytes no consumer reads.
export async function GET(): Promise<Response> {
	const solutions = loadSolutions().map((solution) => {
		const nav = { ...solution };
		delete nav.dashboard;
		return nav;
	});
	return Response.json({ solutions });
}
