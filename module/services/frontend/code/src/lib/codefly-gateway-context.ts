import {
	getCurrentModule,
	getCurrentService,
	getEndpoints,
	getWorkspaceConfiguration,
	getWorkspaceSecret,
	type ServiceEndpoint,
} from "codefly";

export interface CodeflyGatewayContext {
	internalToken: string;
	publicOrigin: string;
}

export interface CodeflyRuntimeReader {
	currentModule(): string;
	currentService(): string;
	endpoints(): ServiceEndpoint[];
	workspaceSecret(name: string, key: string): string | undefined;
	workspaceConfiguration(name: string, key: string): string | undefined;
	/**
	 * Whether this is a deployed build. Next sets NODE_ENV at build time —
	 * "development" under `next dev`, "production" under `next build` — and the
	 * service's execution profiles map every local profile to a development build
	 * and the deployed one to a production build. Nothing a caller sends can
	 * change it.
	 */
	isDeployedBuild(): boolean;
}

const runtimeSDK: CodeflyRuntimeReader = {
	currentModule: getCurrentModule,
	currentService: getCurrentService,
	endpoints: getEndpoints,
	workspaceSecret: getWorkspaceSecret,
	workspaceConfiguration: getWorkspaceConfiguration,
	isDeployedBuild: () => process.env.NODE_ENV === "production",
};

/**
 * The one spelling of an origin, shared with the accounts service.
 *
 * `URL.origin` is the agreement: host case, a default or leading-zero port, an
 * expanded IPv6 literal and a Unicode hostname all settle here, and the Go half
 * reproduces exactly these answers (R1019-N12). The vectors are asserted on both
 * sides and held identical by module/tools/public_origin_vectors_lockstep_test.go.
 *
 * Port 0 is the one place this is deliberately stricter than the parser, which
 * accepts and keeps it: no origin can be served on port 0, and the same range check
 * on both sides is worth more than matching the parser on a value that cannot occur.
 */
// writtenHostname is the host as the operator SPELLED it, lowercased, with no
// parsing beyond finding the authority. It exists so canonicalOrigin can tell a
// dotted quad that was WRITTEN as one from a dotted quad the parser produced out
// of an integer spelling, which is the only question the rule needs answered.
function writtenHostname(candidate: string): string {
	const afterScheme = candidate.slice(candidate.indexOf("//") + 2);
	const authority = afterScheme.split(/[/?#]/, 1)[0] ?? "";
	const hostAndPort = authority.slice(authority.lastIndexOf("@") + 1);
	if (hostAndPort.startsWith("[")) {
		return hostAndPort.slice(0, hostAndPort.indexOf("]") + 1).toLowerCase();
	}
	const colon = hostAndPort.indexOf(":");
	return (colon === -1 ? hostAndPort : hostAndPort.slice(0, colon)).toLowerCase();
}

function canonicalOrigin(candidate: string): string | undefined {
	try {
		const parsed = new URL(candidate);
		if (
			!["http:", "https:"].includes(parsed.protocol) ||
			parsed.username ||
			parsed.password ||
			(parsed.pathname !== "" && parsed.pathname !== "/") ||
			parsed.search ||
			parsed.hash
		) {
			return undefined;
		}
		if (parsed.port !== "") {
			const port = Number(parsed.port);
			if (!Number.isInteger(port) || port < 1 || port > 65535) {
				return undefined;
			}
		}
		// A hostname label may not begin or end with a hyphen (RFC 1123). The parser
		// accepts one and Go's IDNA mapping refuses it, which is a spelling the two
		// sides would answer differently (R1019-N12/f3) — so this side refuses it too
		// rather than leaving the disagreement to the comparison. An IPv6 literal is
		// bracketed and has no labels, so it is left alone.
		if (!parsed.hostname.startsWith("[")) {
			for (const label of parsed.hostname.split(".")) {
				if (label.startsWith("-") || label.endsWith("-")) {
					return undefined;
				}
			}
		}
		// A NUMERIC spelling of an IPv4 address is refused, even though this
		// parser accepts it, because it does not keep what was written:
		// `2130706433`, `0x7f000001`, `127.1` and `0177.0.0.1` all come back as
		// `127.0.0.1`, while Go's net/url preserves the original. Both services
		// would be configured with one value and compare two, so every
		// same-origin check fails and sign-in refuses — a configuration that
		// cannot work, accepted at boot.
		//
		// Narrowed to exactly that rewrite: the parsed host is a dotted quad AND
		// it is not what was written. The two rewrites the two sides DO agree on
		// are untouched — an IPv6 literal compresses on both (`[0:0:…:1]` →
		// `[::1]`) and a Unicode hostname encodes on both (`bücher.example` →
		// `xn--bcher-kva.example`) — so this cannot be the broader "the parser
		// changed something" test, which refused both of those.
		if (
			/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(parsed.hostname) &&
			writtenHostname(candidate) !== parsed.hostname
		) {
			return undefined;
		}
		return parsed.origin;
	} catch {
		return undefined;
	}
}

function isLoopbackOrigin(origin: string): boolean {
	const host = new URL(origin).hostname.toLowerCase().replace(/^\[|\]$/g, "");
	if (host === "localhost" || host === "::1") return true;
	const octets = host.split(".");
	return octets.length === 4 && octets[0] === "127";
}

/**
 * The key an operator sets to pin this deployment's public origin. The same key
 * accounts reads for the links it mints, so the origin that binds a sign-in
 * redirect, an authenticator's relying party and an emailed link is one value in
 * one place.
 *
 * Read below as a literal `workspaceConfiguration("application", "APP_BASE_URL")`
 * so module/tools/workspace_configuration_dependencies_test.go can hold the
 * `application` group to this service's manifest: an undeclared group is never
 * delivered, and this read then returns nothing on every deployed cell.
 */
const PUBLIC_ORIGIN_CONFIGURATION = "application/APP_BASE_URL";

/**
 * Resolve the server-side credentials used between the public frontend and the
 * private auth gateway. Codefly's injected representation remains entirely
 * behind sdk-js; this library only consumes typed SDK values.
 *
 * The public origin is OPERATOR CONFIGURATION and is never derived from a request.
 * It is what binds an OAuth redirect, the authenticator relying-party origin, every
 * emailed link and the solution proxy's same-origin comparison, and none of those is
 * safe to bind to a value a caller can name — so this resolves the origin from
 * configuration or refuses, and never from the request.
 *
 * The order is: the configured origin; then the SDK-discovered own endpoint when
 * the render carries a real host; and on a deployed build, nothing else — a
 * loopback placeholder with no configured origin refuses, by name, rather than
 * asking the caller.
 */
export function resolveCodeflyGatewayContext(
	runtime: CodeflyRuntimeReader = runtimeSDK,
): CodeflyGatewayContext | undefined {
	const internalToken = runtime
		.workspaceSecret("internal-auth", "CODEFLY_INTERNAL_TOKEN")
		?.trim();
	if (!internalToken) return undefined;

	const publicOrigin = resolveVerifiedPublicOrigin(runtime);
	if (!publicOrigin) return undefined;
	return { internalToken, publicOrigin };
}

/**
 * The origin alone, for a caller that needs to judge a request against it rather
 * than to stamp trust headers. Exported separately so "this deployment has no
 * verified origin" and "this deployment has no internal token" stay two different
 * answers: a route that conflated them would refuse a same-origin request for a
 * missing credential it was not asking about.
 */
export function resolveVerifiedPublicOrigin(
	runtime: CodeflyRuntimeReader = runtimeSDK,
): string | undefined {
	return resolvePublicOrigin(runtime);
}

function resolvePublicOrigin(
	runtime: CodeflyRuntimeReader,
): string | undefined {
	const configured = runtime
		.workspaceConfiguration("application", "APP_BASE_URL")
		?.trim();
	if (configured) {
		const origin = canonicalOrigin(configured);
		if (!origin) {
			console.error(
				`${PUBLIC_ORIGIN_CONFIGURATION} is not an exact http(s) origin, so this deployment has no verified public origin; sign-in and the solution proxy are refused until it is corrected`,
			);
			return undefined;
		}
		return origin;
	}

	const endpointOrigin = ownEndpointOrigin(runtime);

	// A loopback own endpoint is correct in local development — the browser
	// reaches that same host — and is the render's placeholder everywhere else.
	// Only the build tells those apart, and nothing a caller sends does.
	if (!runtime.isDeployedBuild()) {
		return endpointOrigin;
	}

	// On a deployed build the own endpoint answers only when the render carries a
	// real ingress host: HTTPS, and neither loopback nor cluster-internal. A
	// cluster-internal address (`frontend.<namespace>.svc.cluster.local`) is what
	// the SDK reports for this workload inside a cell; no browser reaches it, and
	// accounts refuses it against its configured origin. Presenting it turned an
	// undelivered APP_BASE_URL into an opaque "invalid trusted public origin" at
	// accounts, so it is refused here, by name.
	const unusable = endpointOrigin
		? unusableDeployedEndpoint(endpointOrigin)
		: "no single own HTTP endpoint was discovered";
	if (endpointOrigin && !unusable) {
		return endpointOrigin;
	}

	console.error(
		`${PUBLIC_ORIGIN_CONFIGURATION} is not set on this deployed build and its own endpoint cannot stand in for it: ${unusable}. Expected ${PUBLIC_ORIGIN_CONFIGURATION} to carry this cell's public https origin (the frontend must declare the "application" workspace configuration group for it to be delivered). Sign-in and the solution proxy are refused until then, rather than binding them to a host taken from the request.`,
	);
	return undefined;
}

/**
 * Why a deployed build's own endpoint cannot be its public origin, or undefined
 * when it can.
 */
function unusableDeployedEndpoint(origin: string): string | undefined {
	if (isLoopbackOrigin(origin)) {
		return `${origin} is the render's loopback placeholder`;
	}
	const host = new URL(origin).hostname.toLowerCase().replace(/\.$/, "");
	if (
		host.endsWith(".svc") ||
		host.endsWith(".cluster.local") ||
		// A single-label name is a service name resolved by the cluster's DNS; an
		// IPv6 literal (bracketed) has no labels and is judged by scheme below.
		(!host.startsWith("[") && !host.includes("."))
	) {
		return `${origin} is a cluster-internal address, which no browser reaches`;
	}
	if (new URL(origin).protocol !== "https:") {
		return `${origin} is not https, and a non-loopback public origin must be`;
	}
	return undefined;
}

function ownEndpointOrigin(runtime: CodeflyRuntimeReader): string | undefined {
	const currentModule = runtime.currentModule().trim();
	const currentService = runtime.currentService().trim();
	// Outside a Codefly runtime (isolated component tests) there is no own
	// endpoint to discover.
	if (!currentModule && !currentService) return undefined;

	const matches = runtime
		.endpoints()
		.filter(
			(endpoint) =>
				endpoint.module === currentModule &&
				endpoint.service === currentService &&
				endpoint.name === "http" &&
				endpoint.protocol === "HTTP",
		);
	// A Codefly runtime identity must have exactly one SDK-discovered own HTTP
	// endpoint.
	if (matches.length !== 1) return undefined;
	return canonicalOrigin(matches[0].address);
}
