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
