/**
 * What a relative plugin service path may be, and where it lands upstream.
 *
 * Two independent implementations enforced this before: the browser transport
 * in `@codefly-dev/saas-plugin-react` and the host's BFF. They agreed by
 * coincidence, and a route the client admitted that the host refused (or the
 * reverse) was a contract the pair could not express. The rule lives here, in
 * the package both already depend on, so one change moves both sides.
 */

/** A path segment with no reserved character in it. */
const SAFE_SEGMENT = /^[A-Za-z0-9._~-]+$/;

/**
 * Google's custom-verb form (AIP-136): a method that is not one of the
 * resource's standard verbs is bound as `…/<collection>:<verb>`, and the colon
 * is literal — it is what separates the verb from the resource, so a transport
 * that percent-encodes or refuses it cannot reach the binding at all. Only the
 * LAST segment may carry one, exactly one colon, with a lowerCamelCase verb.
 */
const CUSTOM_VERB_SEGMENT = /^[A-Za-z0-9._~-]+:[a-z][A-Za-z0-9]*$/;

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
 * Whether these relative segments are a path the transport may carry: one or
 * more safe segments, the last of which may also be a custom verb.
 */
export function isFrontendServicePath(segments: readonly string[]): boolean {
	if (!Array.isArray(segments) || segments.length === 0) return false;
	return segments.every((segment, index) => {
		if (!usableSegment(segment)) return false;
		return index === segments.length - 1
			? SAFE_SEGMENT.test(segment) || CUSTOM_VERB_SEGMENT.test(segment)
			: SAFE_SEGMENT.test(segment);
	});
}

/** Whether these relative segments name exactly one Connect unary procedure. */
export function isConnectProcedurePath(segments: readonly string[]): boolean {
	return (
		Array.isArray(segments) &&
		segments.length === 2 &&
		segments.every(usableSegment) &&
		CONNECT_SERVICE.test(segments[0]) &&
		CONNECT_METHOD.test(segments[1])
	);
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
