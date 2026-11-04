import { networkInstance, NetworkInstanceAmbiguousError } from "codefly";

/**
 * The API gateway as the composition declares it — the one address a server
 * route may dial.
 *
 * A server-side fetch is never built from the incoming request: its Host
 * header is client-controlled (an SSRF sink), and on a cell the public origin
 * it names terminates TLS at the edge, so dialling it from inside the pod
 * fails the handshake (`SSL routines … wrong version number`) and the route
 * reports an outage that is really a misdirected call. The composed endpoint
 * is plain HTTP inside the mesh, which carries the transport security.
 *
 * `gatewayBase()` keeps a base path the endpoint may carry (a trailing slash
 * trimmed); `gatewayOrigin()` is the origin alone. Both are null when the
 * composition states no gateway, which a caller turns into a visible refusal,
 * never a fallback.
 */
export function gatewayBase(): string | null {
	try {
		const endpoint = networkInstance({ service: "auth-gateway", api: "rest", protocol: "REST" });
		const address = new URL(endpoint.address);
		if (!['http:', 'https:'].includes(address.protocol) || address.username || address.password || address.search || address.hash) return null;
		return address.toString().replace(/\/$/, "");
	} catch (error) {
		if (error instanceof NetworkInstanceAmbiguousError) {
			console.error("gateway resolution: ambiguous auth-gateway/rest/REST endpoint in the current module");
		}
		return null;
	}
}

export function gatewayOrigin(): string | null {
	const base = gatewayBase();
	return base ? new URL(base).origin : null;
}
