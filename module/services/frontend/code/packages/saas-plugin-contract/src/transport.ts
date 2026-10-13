/**
 * What a relative plugin service path may be, and where it lands upstream.
 *
 * Two independent implementations enforced this before: the browser transport
 * in `@codefly-dev/saas-plugin-react` and the host's BFF. They agreed by
 * coincidence, and a route the client admitted that the host refused (or the
 * reverse) was a contract the pair could not express. The rule lives here, in
 * the package both already depend on, so one change moves both sides.
 */

import type { FrontendServiceProtocol } from "./contracts.js";

/** A path segment with no reserved character in it. */
const SAFE_SEGMENT = /^[A-Za-z0-9._~-]+$/;

/**
 * Google's custom-verb form (AIP-136): a method that is not one of the
 * resource's standard verbs is bound as `…/<collection>:<verb>`, and the colon
 * is literal — it is what separates the verb from the resource, so a transport
 * that percent-encodes or refuses it cannot reach the binding at all. Only the
 * LAST segment may carry one, exactly one colon, with a lowerCamelCase verb.
 */
const CUSTOM_VERB = /^[a-z][A-Za-z0-9]*$/;

/**
 * The Connect protocol's own path shape: `/<fully.qualified.Service>/<Method>`,
 * a proto full name and a method name. A Connect alias serves nothing else, so
 * this is a tighter bound than a generic safe path rather than a new freedom.
 */
const CONNECT_SERVICE = /^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+$/;
const CONNECT_METHOD = /^[A-Za-z_][A-Za-z0-9_]*$/;

const MAX_SEGMENT_LENGTH = 256;

/**
 * The origin root. A requirement declares it as its route prefix to say that
 * its procedures are served where the Connect protocol puts them — which is
 * also where the fixed capability handshake already lives — rather than under a
 * product path prefix.
 */
export const FRONTEND_SERVICE_ROOT_PREFIX = "/";

/**
 * An absolute upstream path prefix: one or more safe segments, each with no
 * reserved character in it. Deliberately narrower than a URL path — a prefix is
 * where the host mounts an alias, so a query, a fragment, an empty segment or a
 * trailing slash are all shapes it has no meaning for.
 */
const SAFE_ROUTE_PREFIX = /^\/[A-Za-z0-9._~-]+(?:\/[A-Za-z0-9._~-]+)*$/;

/**
 * Whether this requirement's route prefix is one the host may mount, for the
 * protocol it declares. It is the WHOLE rule, in one place, because two
 * different readings of it are what the BFF walked past: the composition
 * validated the full form at build time while the runtime re-check compared
 * only against the root, so a prefix the composition would have refused —
 * `""`, `/..`, `/a//b` — passed the runtime one and landed its relative path
 * straight on the upstream origin.
 *
 * The origin root is Connect-only, and declaring it is the only way to say what
 * the Connect protocol already decides: a procedure's path is
 * `/<fully.qualified.Service>/<Method>` from the origin. A REST requirement
 * declaring it would be a prefix-less proxy of its whole upstream, and so would
 * any prefix that is not an absolute path of safe, non-dot segments.
 */
export function isFrontendServiceRoutePrefix(
	protocol: FrontendServiceProtocol,
	routePrefix: unknown,
): routePrefix is string {
	if (typeof routePrefix !== "string") return false;
	if (protocol === "connect" && routePrefix === FRONTEND_SERVICE_ROOT_PREFIX) {
		return true;
	}
	return (
		SAFE_ROUTE_PREFIX.test(routePrefix) &&
		routePrefix
			.slice(1)
			.split("/")
			.every((segment) => segment !== "." && segment !== "..")
	);
}

/**
 * What a requirement must look like for the host to proxy it at all: its prefix
 * is one the protocol admits, and a Connect one declares a usable list of the
 * services it may reach.
 *
 * Both halves are the composition's own rules, and this is the one function
 * that states them — the BFF reads the GENERATED allowlist rather than the
 * composition that validated it, so a build-time rule no runtime re-checks is
 * one a stale or hand-edited projection walks straight past. `Array.isArray` is
 * part of the rule and not defensive noise: a projection whose
 * `connectServices` is a string has a `length` and no `every`, so a check that
 * assumed an array threw out of the request handler as a 500 instead of
 * refusing the alias as unserviceable.
 */
export function isServiceableFrontendService(service: {
	protocol: FrontendServiceProtocol;
	routePrefix: string;
	connectServices?: readonly string[];
}): boolean {
	if (!isFrontendServiceRoutePrefix(service.protocol, service.routePrefix)) {
		return false;
	}
	if (service.protocol !== "connect") return true;
	// Connect without a declared service list reaches nothing: the path names
	// the service, so an absent — or non-array — list is an unbounded alias.
	const services = service.connectServices;
	return (
		Array.isArray(services) &&
		services.length > 0 &&
		services.every(isConnectServiceName)
	);
}

function usableSegment(segment: string): boolean {
	return (
		typeof segment === "string" &&
		segment.length > 0 &&
		segment.length <= MAX_SEGMENT_LENGTH &&
		segment !== "." &&
		segment !== ".."
	);
}

/**
 * A safe segment is one with no reserved character and no dot-segment meaning.
 * The two rules travel together everywhere, including over the resource half of
 * a custom verb: `..:recover` is a traversal wearing a verb, and splitting the
 * segment is the only way the not-dot rule reaches the part that names the
 * resource.
 */
function safeSegment(segment: string): boolean {
	return usableSegment(segment) && SAFE_SEGMENT.test(segment);
}

/**
 * Whether this segment is a resource with a custom verb on it. The resource
 * half obeys every rule a segment of its own would: one colon splits them, so a
 * second colon falls into the verb and fails there.
 */
function customVerbSegment(segment: string): boolean {
	if (!usableSegment(segment)) return false;
	const colon = segment.indexOf(":");
	if (colon < 0) return false;
	return (
		safeSegment(segment.slice(0, colon)) &&
		CUSTOM_VERB.test(segment.slice(colon + 1))
	);
}

/**
 * Whether these relative segments are a path the transport may carry: one or
 * more safe segments, the last of which may also be a custom verb.
 */
export function isFrontendServicePath(segments: readonly string[]): boolean {
	if (!Array.isArray(segments) || segments.length === 0) return false;
	return segments.every((segment, index) =>
		index === segments.length - 1
			? safeSegment(segment) || customVerbSegment(segment)
			: safeSegment(segment),
	);
}

/** Whether this is one fully qualified Connect service name. */
export function isConnectServiceName(value: string): boolean {
	return (
		typeof value === "string" &&
		value.length > 0 &&
		value.length <= MAX_SEGMENT_LENGTH &&
		CONNECT_SERVICE.test(value)
	);
}

/** Whether these relative segments name exactly one Connect unary procedure. */
export function isConnectProcedurePath(segments: readonly string[]): boolean {
	return (
		Array.isArray(segments) &&
		segments.length === 2 &&
		segments.every(usableSegment) &&
		isConnectServiceName(segments[0]) &&
		CONNECT_METHOD.test(segments[1])
	);
}

/** One Connect procedure a requirement admits. */
export interface ConnectProcedure {
	readonly service: string;
	readonly method: string;
}

/**
 * The procedure these relative segments name, for a Connect requirement that
 * declared which services it may reach — or `null` when they name none it may.
 *
 * On a Connect alias the caller supplies the service name in the path, because
 * that is where the protocol puts it. Nothing else bounds which service that
 * is: `routePrefix` says where the upstream mounts Connect, not which of its
 * services an alias speaks to, so a requirement that declared only a prefix
 * could reach every service the origin serves, as the viewer. `connectServices`
 * is that bound, and it is the requirement's to declare rather than the
 * caller's to choose. A requirement that reaches exactly one service — the
 * shape a `/<pkg.Service>` prefix used to express — declares one entry.
 *
 * Fails closed: an empty list, a list with an unusable name in it, or anything
 * that is not one procedure path admits nothing.
 */
export function resolveConnectProcedure(
	services: readonly string[],
	segments: readonly string[],
): ConnectProcedure | null {
	if (!Array.isArray(services) || services.length === 0) return null;
	if (!services.every(isConnectServiceName)) return null;
	if (!isConnectProcedurePath(segments)) return null;
	const [service, method] = segments;
	return services.includes(service) ? { service, method } : null;
}

/**
 * The relative path as a URL carries it. Every segment is percent-encoded, and
 * a custom verb's colon is restored: `:` is a legal path character (RFC 3986
 * `pchar`) and the binding is only reachable with it literal.
 */
export function encodeFrontendServicePath(segments: readonly string[]): string {
	return segments
		.map((segment) => encodeURIComponent(segment).replaceAll("%3A", ":"))
		.join("/");
}

/**
 * Where a declared binding's relative path lands on the upstream: the
 * requirement's own prefix, or nothing at all when that prefix is the root.
 */
export function frontendServiceTargetPath(
	routePrefix: string,
	segments: readonly string[],
): string {
	const prefix =
		routePrefix === FRONTEND_SERVICE_ROOT_PREFIX ? "" : routePrefix;
	const relative = encodeFrontendServicePath(segments);
	if (!relative) return prefix || FRONTEND_SERVICE_ROOT_PREFIX;
	return `${prefix}/${relative}`;
}

/**
 * Where one admitted Connect procedure lands on the upstream: under the
 * requirement's mount prefix, at the path the protocol puts it.
 */
export function connectProcedureTargetPath(
	routePrefix: string,
	procedure: ConnectProcedure,
): string {
	return frontendServiceTargetPath(routePrefix, [
		procedure.service,
		procedure.method,
	]);
}
