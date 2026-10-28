import { isHostSetupProcedure } from "@/lib/host-setup-procedures";
import { gatewayBase } from "@/lib/gateway-endpoint";

import {
	resolveCodeflyGatewayContext,
	resolveVerifiedPublicOrigin,
} from "@/lib/codefly-gateway-context";
import { INTERNAL_TOKEN_HEADER } from "@/lib/internal-token";
import { forwardTraceContext } from "@/lib/trace-context";
import { findSolution } from "@/solutions/registry";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";
const MAX_RESUME_BYTES = 1024;

interface RouteContext {
	params: Promise<{ id: string; path?: string[]; version?: string }>;
}

// This route forwards the caller's cookies to the gateway under a trusted
// internal token, so it must reject cross-site requests itself — cookies ride
// along automatically on a forged cross-origin call, and the gateway cannot tell
// a CSRF-driven request from a legitimate one once the internal token is
// attached.
//
// The browser's Origin is compared with this deployment's CONFIGURED public
// origin, which comes from operator configuration. The comparison is only a check
// when one side is not the caller's to choose, so the side it compares against is
// never taken from the request.
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

/**
 * Generic solution proxy. Forwards a browser request to the API gateway's
 * root for platform Connect procedures, /v1/{alias}/… for module data, or
 * /solutions/{alias}/… for solution data and shared assets, attaching first-party
 * trust headers and carrying the caller's identity (bearer and/or session
 * cookie). The browser never reaches a solution service directly, and this route
 * names no specific solution — it only resolves whatever registered at runtime.
 */
async function handler(
	request: Request,
	context: RouteContext,
): Promise<Response> {
	const { id, path, version } = await context.params;
	const moduleCall = path?.[0] === "_browser";
	// Retained code identities open assets only, never the separate module edge.
	if (moduleCall && version !== undefined) {
		return new Response("versioned module calls are not served", { status: 404 });
	}

	// Fetch normalizes dot segments before the gateway sees them. Validate this
	// dedicated Connect entry before constructing a trusted URL so no suffix can
	// remove the terminal marker. Publication and authority remain gateway-owned.
	if (moduleCall && (
		request.method !== "POST" || path.length !== 3 ||
		! /^(?:[A-Za-z_][A-Za-z0-9_]*\.)+[A-Za-z_][A-Za-z0-9_]*$/.test(path[1]) ||
		! /^[A-Za-z_][A-Za-z0-9_]*$/.test(path[2])
	)) {
		return new Response("invalid module procedure", { status: 404 });
	}

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

	// A retained asset remains routable when the current generation is
	// headless. The gateway owns target liveness and retained admission.
	const retainedAsset = version !== undefined &&
		(request.method === "GET" || request.method === "HEAD") && path?.[0] === "assets";
	const solution = retainedAsset ? null : await findSolution(id);
	// Distinguish a solution that is not registered from a registry this
	// replica cannot read: the first is permanent for the caller, the second is
	// worth retrying.
	if (solution === "unavailable") {
		return new Response("solution registry unavailable", { status: 503 });
	}
	if (!solution && !retainedAsset) {
		return new Response("solution not registered", { status: 404 });
	}
	if (
		version !== undefined &&
		(!/^[a-f0-9]{64}$/.test(version) || (!retainedAsset && version !== solution?.assetVersion))
	) {
		return new Response("frontend generation is no longer served", {
			status: 410,
			headers: { "cache-control": "no-store" },
		});
	}
	const base = gatewayBase();
	if (!base) {
		return new Response("gateway unavailable", { status: 502 });
	}

	const suffix = (path ?? []).map(encodeURIComponent).join("/");
	const search = new URL(request.url).search;
	// The host's own Connect procedures, at the gateway root: its saas.*
	// services, and the Setup tab's host installation procedures a solution
	// mounting `<SolutionSetup binding>` calls over this same base.
	const platformProcedure =
		/^saas\.[A-Za-z_][A-Za-z0-9_]*\.v1\.[A-Za-z_][A-Za-z0-9_]*\/[A-Za-z_][A-Za-z0-9_]*$/.test(
			suffix,
		) || isHostSetupProcedure(`/${suffix}`);
	// Both kinds use the same page and asset loader. A module's data requests
	// stay on the module gateway surface, preserving its audience and checks.
	// The gateway independently restricts the common asset route to public
	// GET/HEAD paths; this choice grants no exemption here.
	const publicAsset =
		(request.method === "GET" || request.method === "HEAD") &&
		/^(?:assets|\.well-known)(?:\/|$)/.test(suffix);
	const routePrefix =
		solution?.kind === "module" && !publicAsset ? "v1" : "solutions";
	// Preserve the marker: only the gateway's terminal public-only dispatch
	// may remove it. Sending an arbitrary suffix to the root would expose its
	// internal exchanges under this proxy's trust context.
	const target = moduleCall || platformProcedure
		? `${base}/${suffix}${search}`
		: `${base}/${routePrefix}/${encodeURIComponent((solution?.backend.serviceAlias ?? id))}/${suffix}${search}`;

	const headers = new Headers();
	// Accounts-backed retained history at the gateway decides asset admission.
	// Forward the requested identity without relabelling it as the current version.
	if (version !== undefined) headers.set("X-Codefly-Frontend-Version", version);
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
	if (moduleCall) {
		for (const name of ["connect-protocol-version", "connect-timeout-ms", "connect-content-encoding", "connect-accept-encoding"]) {
			const value = request.headers.get(name);
			if (value !== null) headers.set(name, value);
		}
	}
	headers.set("accept", request.headers.get("accept") ?? "application/json");

	if (resume !== null) headers.set("last-event-id", resume);
	// Continue the caller's W3C trace so the solution's spans join it. Only a
	// strictly valid traceparent (and the tracestate riding with it) travels,
	// re-serialized rather than copied; baggage is never forwarded.
	//
	// The sampled flag is an instruction to the upstream's sampler, not inert
	// metadata: OpenTelemetry's default ParentBased sampler records whatever an
	// incoming traceparent declares sampled. This route admits a request that
	// carries neither Origin nor Sec-Fetch-Site, and it serves a solution's
	// `.well-known` anonymously (MODULE.md), on a request it has already
	// stamped with the internal trust token — so honouring that bit for a
	// caller who presented no credential at all would let a stranger force 100%
	// recording and export on the gateway and on every solution behind it.
	//
	// A caller that did present one keeps the decision: the gateway verifies the
	// credential, so a forged one costs a single rejected request rather than an
	// open lever. The trace still links either way; only the instruction is
	// withheld.
	forwardTraceContext(request.headers, headers, {
		honorSampledFlag: authorization !== null || cookie !== null,
	});

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
			`solution proxy: gateway unreachable solution=${id} alias=${(solution?.backend.serviceAlias ?? id)} path=${suffix} method=${request.method}`,
			err,
		);
		return new Response("solution gateway unreachable", { status: 502 });
	}

	const responseHeaders = new Headers();
	if (moduleCall) {
		for (const name of ["connect-content-encoding", "connect-accept-encoding"]) {
			const value = upstream.headers.get(name);
			if (value !== null) responseHeaders.set(name, value);
		}
	}
 if (retainedAsset) {
  responseHeaders.set("cache-control", upstream.headers.get("cache-control") ?? "no-store");
  responseHeaders.set("x-content-type-options", "nosniff");
 }
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
	handler as HEAD,
	handler as PATCH,
	handler as POST,
	handler as PUT,
};
