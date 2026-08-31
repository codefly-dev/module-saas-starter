import { getEndpoints } from "codefly";

import { findSolution } from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

interface RouteContext {
	params: Promise<{ id: string; path?: string[] }>;
}

/** Resolve the auth-sidecar API gateway base from the Codefly SDK. */
function gatewayBase(): string | null {
	const endpoint = getEndpoints().find(
		(candidate) =>
			candidate.service === "auth-sidecar" && candidate.name === "rest",
	);
	if (!endpoint?.address) {
		return null;
	}
	try {
		return new URL(endpoint.address).origin;
	} catch {
		return null;
	}
}

/**
 * Generic solution proxy. Forwards a browser request to the API gateway's
 * runtime solution passthrough (/solutions/{alias}/…), carrying the caller's
 * bearer. The browser never reaches a solution service directly, and this route
 * names no specific solution — it only resolves whatever registered at runtime.
 */
async function handler(
	request: Request,
	context: RouteContext,
): Promise<Response> {
	const { id, path } = await context.params;

	const solution = findSolution(id);
	if (!solution) {
		return new Response("solution not registered", { status: 404 });
	}
	const base = gatewayBase();
	if (!base) {
		return new Response("gateway unavailable", { status: 502 });
	}

	const suffix = (path ?? []).map(encodeURIComponent).join("/");
	const search = new URL(request.url).search;
	const target = `${base}/solutions/${encodeURIComponent(solution.backend.serviceAlias)}/${suffix}${search}`;

	const headers = new Headers();
	const authorization = request.headers.get("authorization");
	if (authorization) {
		headers.set("authorization", authorization);
	}
	const contentType = request.headers.get("content-type");
	if (contentType) {
		headers.set("content-type", contentType);
	}
	headers.set("accept", request.headers.get("accept") ?? "application/json");

	const init: RequestInit = { method: request.method, headers };
	if (request.method !== "GET" && request.method !== "HEAD") {
		init.body = await request.arrayBuffer();
	}

	let upstream: Response;
	try {
		upstream = await fetch(target, init);
	} catch (err) {
		// The gateway resolved but is unreachable (DNS, refused, reset). Distinct
		// from an unresolvable endpoint above so an operator can tell "no gateway
		// configured" from "gateway down".
		console.error(
			`solution proxy: gateway unreachable solution=${id} alias=${solution.backend.serviceAlias}`,
			err,
		);
		return new Response("solution gateway unreachable", { status: 502 });
	}

	const responseHeaders = new Headers();
	const upstreamContentType = upstream.headers.get("content-type");
	if (upstreamContentType) {
		responseHeaders.set("content-type", upstreamContentType);
	}
	const upstreamRequestID = upstream.headers.get("x-request-id");
	if (upstreamRequestID) {
		responseHeaders.set("x-request-id", upstreamRequestID);
	}

	// A forwarded error keeps its upstream status and body (the solution's remote
	// owns how it renders them), but the host categorizes it so an operator can
	// tell auth (401/403) from the solution's own upstream (5xx) — and correlates
	// each to the gateway via its request id.
	if (!upstream.ok) {
		const category = errorCategory(upstream.status);
		responseHeaders.set("x-codefly-solution-error", category);
		console.error(
			`solution proxy: upstream error solution=${id} status=${upstream.status} category=${category} request_id=${upstreamRequestID ?? ""}`,
		);
	}

	return new Response(upstream.body, {
		status: upstream.status,
		headers: responseHeaders,
	});
}

/** Classify a forwarded gateway status into an operator-facing failure cause. */
function errorCategory(status: number): string {
	if (status === 401 || status === 403) return "auth";
	if (status === 404) return "not_registered";
	if (status >= 500) return "upstream";
	return "request";
}

export {
	handler as DELETE,
	handler as GET,
	handler as PATCH,
	handler as POST,
	handler as PUT,
};
