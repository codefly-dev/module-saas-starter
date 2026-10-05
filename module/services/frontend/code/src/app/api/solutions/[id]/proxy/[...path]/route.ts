import { getEndpoints } from "codefly";

import {
	resolveCodeflyGatewayContext,
	resolveVerifiedPublicOrigin,
} from "@/lib/codefly-gateway-context";
import { INTERNAL_TOKEN_HEADER } from "@/lib/internal-token";

import { findSolution } from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";
const MAX_RESUME_BYTES = 1024;

interface RouteContext {
	params: Promise<{ id: string; path?: string[] }>;
}

// This route forwards the caller's cookies to the gateway under a trusted
// internal token, so it must reject cross-site requests itself — cookies ride
// along automatically on a forged cross-origin call, and the gateway cannot tell
// a CSRF-driven request from a legitimate one once the internal token is
// attached.
//
// The browser's Origin is compared with this deployment's CONFIGURED public
// origin. It used to be compared with an origin derived from the request's own
// forwarded headers, which made the check self-referential: a non-browser caller
// supplying `Origin: https://evil.example` and `X-Forwarded-Host: evil.example`
// matched itself and passed. The comparison is only a check when one side is not
// the caller's to choose.
//
// A deployment with no verified public origin refuses: there is nothing to compare
// against, and admitting on that basis is the same defect by omission.
function sameOrigin(request: Request, hostOrigin: string | undefined): boolean {
	if (!hostOrigin) return false;
	const origin = request.headers.get("origin");
	if (origin) {
		try {
			if (new URL(origin).origin !== hostOrigin) return false;
		} catch {
			return false;
		}
	}
	const fetchSite = request.headers.get("sec-fetch-site");
	return !fetchSite || fetchSite === "same-origin" || fetchSite === "none";
}

/**
 * Resolve the auth-gateway API gateway base from the Codefly SDK.
 *
 * Normalised the same way `server/accounts-bindings.mjs` normalises it for
 * `src/proxy.ts` — the whole address minus a trailing slash, NOT its origin.
 * A base path on the endpoint is load-bearing: `proxy.ts` forwards a host
 * page's own product API call to `${rest}${pathname}`, so discarding the path
 * here would send a solution's platform procedure to a different URL than the
 * byte-identical call issued from a host page, against the same composition.
 */
function gatewayBase(): string | null {
	const endpoint = getEndpoints().find(
		(candidate) =>
			candidate.service === "auth-gateway" && candidate.name === "rest",
	);
	if (!endpoint?.address) {
		return null;
	}
	try {
		return new URL(endpoint.address).toString().replace(/\/$/, "");
	} catch {
		return null;
	}
}

/**
 * Generic solution proxy. Forwards a browser request to the API gateway's
 * root for platform Connect procedures or its runtime solution passthrough
 * (/solutions/{alias}/…) for other paths, attaching the first-party
 * trust headers and carrying the caller's identity (bearer and/or session
 * cookie). The browser never reaches a solution service directly, and this route
 * names no specific solution — it only resolves whatever registered at runtime.
 */
async function handler(
	request: Request,
	context: RouteContext,
): Promise<Response> {
	const { id, path } = await context.params;

	// The origin is resolved on its own, not off the trust context: a deployment
	// missing the internal token is a different condition from one with no verified
	// origin, and refusing a same-origin request for the first would be answering a
	// question nobody asked.
	if (!sameOrigin(request, resolveVerifiedPublicOrigin())) {
		return new Response("cross-origin request rejected", { status: 403 });
	}

	// A cursor is only an observation hint. It never supplies identity or
	// authorizes a retry. Keep its carrier bounded before contacting the gateway.
	const resume =
		request.method === "GET" ? request.headers.get("last-event-id") : null;
	if (
		resume !== null &&
		(new TextEncoder().encode(resume).length > MAX_RESUME_BYTES ||
			/[\u0000-\u001f\u007f]/.test(resume))
	) {
		return new Response("invalid event cursor", { status: 400 });
	}

	const solution = await findSolution(id);
	// Distinguish a solution that is not registered from a registry this
	// replica cannot read: the first is permanent for the caller, the second is
	// worth retrying.
	if (solution === "unavailable") {
		return new Response("solution registry unavailable", { status: 503 });
	}
	if (!solution) {
		return new Response("solution not registered", { status: 404 });
	}
	const base = gatewayBase();
	if (!base) {
		return new Response("gateway unavailable", { status: 502 });
	}

	const suffix = (path ?? []).map(encodeURIComponent).join("/");
	const search = new URL(request.url).search;
	const platformProcedure =
		/^saas\.[A-Za-z_][A-Za-z0-9_]*\.v1\.[A-Za-z_][A-Za-z0-9_]*\/[A-Za-z_][A-Za-z0-9_]*$/.test(
			suffix,
		);
	const target = platformProcedure
		? `${base}/${suffix}${search}`
		: `${base}/solutions/${encodeURIComponent(solution.backend.serviceAlias)}/${suffix}${search}`;

	const headers = new Headers();
	// First-party trust headers, resolved server-side from Codefly config — the
	// gateway rejects solution traffic that lacks them even with a valid user
	// identity. Set from a fresh Headers so a caller can never spoof them.
	const gatewayContext = resolveCodeflyGatewayContext();
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

	if (resume !== null) headers.set("last-event-id", resume);

	// Preserve the browser connection lifetime and avoid cached observations or
	// forwarding its credentials through a gateway redirect. Never retry here.
	const init: RequestInit = {
		method: request.method,
		headers,
		signal: request.signal,
		cache: "no-store",
		redirect: "error",
	};
	if (request.method !== "GET" && request.method !== "HEAD") {
		init.body = await request.arrayBuffer();
	}

	let upstream: Response;
	try {
		upstream = await fetch(target, init);
	} catch (err) {
		if (request.signal?.aborted) {
			return new Response(null, { status: 499 });
		}
		// The gateway resolved but is unreachable (DNS, refused, reset). Distinct
		// from an unresolvable endpoint above so an operator can tell "no gateway
		// configured" from "gateway down".
		console.error(
			`solution proxy: gateway unreachable solution=${id} alias=${solution.backend.serviceAlias} path=${suffix} method=${request.method}`,
			err,
		);
		return new Response("solution gateway unreachable", { status: 502 });
	}

	const responseHeaders = new Headers();
	const upstreamContentType = upstream.headers.get("content-type");
	if (upstreamContentType) {
		responseHeaders.set("content-type", upstreamContentType);
	}
	if (isStreamingContentType(upstreamContentType)) {
		// This authenticated stream must stay incremental. Preserve its bytes and
		// EOF; the solution decides whether a message is terminal or needs a
		// reset. no-transform keeps a compressing proxy from holding bytes to fill
		// a block; x-accel-buffering keeps an ingress from holding the response.
		responseHeaders.set("cache-control", "no-store, no-transform");
		responseHeaders.set("x-accel-buffering", "no");
	}
	const upstreamRequestID = upstream.headers.get("x-request-id");
	if (upstreamRequestID) {
		responseHeaders.set("x-request-id", upstreamRequestID);
	}
	// The gateway's authentication challenge, passed through verbatim.
	//
	// This is how an MCP client starts discovery: a 401 names where the resource
	// describes itself, that document names the authorization server, and the
	// client runs the authorization-code flow. This route is the only public way
	// to a solution's backend, so a challenge it drops is a challenge nobody can
	// ever read — the client sees a bare 401 and reports "unauthorized" with
	// nowhere to go. RFC 6750 §3 and RFC 9728 §5.1 both require the header to
	// reach the caller.
	//
	// Forwarded on any status that carries it rather than only on 401: a 403 may
	// legitimately carry one too, and the header is the gateway's statement
	// about the credential, not this route's to interpret.
	const challenge = upstream.headers.get("www-authenticate");
	if (challenge) {
		responseHeaders.set("www-authenticate", challenge);
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
		// The path is the whole diagnosis. Without it a run of these lines says
		// only that *something* under a solution failed, and an operator holding
		// them cannot tell one procedure from another — which is exactly how a
		// panel that could never have worked reads the same as an expired token.
		// It is the caller's own suffix, already percent-encoded above, so it
		// carries no identity: a bearer travels in a header and a cursor in
		// `last-event-id`, both deliberately kept out of the URL.
		log(
			`solution proxy: upstream error solution=${id} path=${suffix} method=${request.method} status=${upstream.status} category=${category} request_id=${upstreamRequestID ?? ""}`,
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
/**
 * Whether a response is a stream whose messages must reach the caller as they
 * are written: Server-Sent Events, a Connect streaming RPC
 * (`application/connect+json`, `application/connect+proto`) and gRPC-web
 * (`application/grpc-web`, `+proto`, `-text`, …). A unary Connect or REST
 * response (`application/json`, `application/proto`) is not one.
 */
function isStreamingContentType(contentType: string | null): boolean {
	const essence = contentType?.split(";")[0].trim().toLowerCase() ?? "";
	return (
		essence === "text/event-stream" ||
		essence.startsWith("application/connect+") ||
		essence === "application/grpc-web" ||
		essence.startsWith("application/grpc-web+") ||
		essence.startsWith("application/grpc-web-text")
	);
}

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
