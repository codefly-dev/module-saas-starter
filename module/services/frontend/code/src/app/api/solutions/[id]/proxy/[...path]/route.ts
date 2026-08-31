import { getEndpoints } from "codefly";

import { resolveCodeflyGatewayContext } from "@/lib/codefly-gateway-context";
import { findSolution } from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

const INTERNAL_TOKEN_HEADER = "X-Codefly-Internal-Token";
const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";

interface RouteContext {
	params: Promise<{ id: string; path?: string[] }>;
}

// The public browser origin, preferring the ingress-set forwarded pair over the
// pod-local request URL — the same resolution src/proxy.ts uses, because behind
// a TLS-terminating ingress the pod sees plaintext `http` and its own host.
function callerOrigin(request: Request): string {
	const url = new URL(request.url);
	const forwardedProto = request.headers
		.get("x-forwarded-proto")
		?.split(",")[0]
		?.trim();
	const forwardedHost = request.headers
		.get("x-forwarded-host")
		?.split(",")[0]
		?.trim();
	const protocol = forwardedProto ? `${forwardedProto}:` : url.protocol;
	const host = forwardedHost || url.host;
	return `${protocol}//${host}`;
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
 * runtime solution passthrough (/solutions/{alias}/…), attaching the first-party
 * trust headers and carrying the caller's identity (bearer and/or session
 * cookie). The browser never reaches a solution service directly, and this route
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
	// First-party trust headers, resolved server-side from Codefly config — the
	// gateway rejects solution traffic that lacks them even with a valid user
	// identity. Set from a fresh Headers so a caller can never spoof them.
	const gatewayContext = resolveCodeflyGatewayContext(callerOrigin(request));
	if (gatewayContext) {
		headers.set(INTERNAL_TOKEN_HEADER, gatewayContext.internalToken);
		headers.set(PUBLIC_ORIGIN_HEADER, gatewayContext.publicOrigin);
	}
	// Carry the caller's identity to the gateway. A solution remote may attach
	// the host access token (Authorization) or authenticate with the session
	// cookie; forward both so the gateway can resolve the user either way.
	const authorization = request.headers.get("authorization");
	if (authorization) {
		headers.set("authorization", authorization);
	}
	const cookie = request.headers.get("cookie");
	if (cookie) {
		headers.set("cookie", cookie);
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
		// A 5xx is the solution's own fault and warrants error-level attention; a
		// 4xx is a client/auth condition (e.g. an expired token on a poll) and
		// would only spam the log at error level.
		const log = upstream.status >= 500 ? console.error : console.warn;
		log(
			`solution proxy: upstream error solution=${id} status=${upstream.status} category=${category} request_id=${upstreamRequestID ?? ""}`,
		);
	}

	return new Response(upstream.body, {
		status: upstream.status,
		headers: responseHeaders,
	});
}

/**
 * Classify a forwarded gateway status into an operator-facing failure cause.
 *
 * A registration miss is caught by the host's own 404 before any upstream call
 * (see `findSolution` above), so a *forwarded* 404 is the solution's own API
 * reporting a missing resource — labeled `not_found`, never `not_registered`,
 * so an operator isn't sent chasing a registration bug that isn't there.
 */
function errorCategory(status: number): string {
	if (status === 401 || status === 403) return "auth";
	if (status === 404) return "not_found";
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
