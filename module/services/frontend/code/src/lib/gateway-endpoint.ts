import { getEndpoints } from "codefly";

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
	const address = gatewayAddress();
	if (!address) return null;
	try {
		return new URL(address).toString().replace(/\/$/, "");
	} catch {
		return null;
	}
}

export function gatewayOrigin(): string | null {
	const address = gatewayAddress();
	if (!address) return null;
	try {
		return new URL(address).origin;
	} catch {
		return null;
	}
}

function gatewayAddress(): string | undefined {
	return getEndpoints().find(
		(candidate) =>
			candidate.service === "auth-gateway" && candidate.name === "rest",
	)?.address;
}
