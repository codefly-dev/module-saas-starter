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
async function handler(request: Request, context: RouteContext): Promise<Response> {
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

	const upstream = await fetch(target, init);
	const responseHeaders = new Headers();
	const upstreamContentType = upstream.headers.get("content-type");
	if (upstreamContentType) {
		responseHeaders.set("content-type", upstreamContentType);
	}
	return new Response(upstream.body, {
		status: upstream.status,
		headers: responseHeaders,
	});
}

export {
	handler as DELETE,
	handler as GET,
	handler as PATCH,
	handler as POST,
	handler as PUT,
};
