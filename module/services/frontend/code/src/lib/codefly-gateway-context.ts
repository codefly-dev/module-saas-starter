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
 */
const PUBLIC_ORIGIN_CONFIGURATION = "application/APP_BASE_URL";

/**
 * Resolve the server-side credentials used between the public frontend and the
 * private auth gateway. Codefly's injected representation remains entirely
 * behind sdk-js; this library only consumes typed SDK values.
 *
 * The public origin is OPERATOR CONFIGURATION and is never derived from a
 * request. It used to be: when the own rendered endpoint was a loopback
 * placeholder — which is what the render produces, so on every cell — this fell
 * back to the caller's own `X-Forwarded-Host` and stamped it, under the internal
 * token, as the verified public origin. accounts then bound an OAuth redirect,
 * the authenticator relying-party origin, and emailed links to a host the caller
 * had chosen, and the solution proxy's same-origin check compared against the
 * same caller-supplied value.
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
	const [group, key] = PUBLIC_ORIGIN_CONFIGURATION.split("/");
	const configured = runtime.workspaceConfiguration(group, key)?.trim();
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

	// A render that carries a real ingress host is operator configuration too, so
	// it answers when nothing is pinned.
	if (endpointOrigin && !isLoopbackOrigin(endpointOrigin)) {
		return endpointOrigin;
	}

	// A loopback own endpoint is correct in local development — the browser
	// reaches that same host — and is the render's placeholder everywhere else.
	// Only the build tells those apart, and nothing a caller sends does.
	if (!runtime.isDeployedBuild()) {
		return endpointOrigin;
	}

	console.error(
		`${PUBLIC_ORIGIN_CONFIGURATION} is not set and this deployment's own endpoint renders as a loopback placeholder, so there is no verified public origin; set it to this cell's public origin. Sign-in and the solution proxy are refused until then, rather than binding them to a host taken from the request.`,
	);
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
