import { getCurrentModule, getEndpoints } from "codefly";

// Direct-Accounts destinations a pre-gateway composition injected. They no
// longer select anything: a value left behind in a real environment must
// surface as a configuration error, never as a quiet second API path.
const RETIRED_DIRECT_VARIABLES = ["API_REST_INTERNAL", "API_CONNECT_INTERNAL"];

function serviceURL(value, variable) {
	let parsed;
	try {
		parsed = new URL(value);
	} catch {
		throw new Error(`${variable} must be an absolute HTTP(S) URL`);
	}
	if (!["http:", "https:"].includes(parsed.protocol)) {
		throw new Error(`${variable} must use HTTP or HTTPS`);
	}
	if (parsed.username || parsed.password || parsed.search || parsed.hash) {
		throw new Error(
			`${variable} cannot contain credentials, a query, or a fragment`,
		);
	}
	return parsed.toString().replace(/\/$/, "");
}

/**
 * The one product API path, or undefined when this composition declares none.
 *
 * PRODUCT_GATEWAY_INTERNAL names the same gateway for a frontend started
 * outside the module graph (the browser suite's own server); it cannot select
 * a different service, so the single-path architecture holds either way.
 */
function configuredGateway(options) {
	const environment = options.environment ?? process.env;
	const present = RETIRED_DIRECT_VARIABLES.filter((variable) =>
		environment[variable]?.trim(),
	);
	if (present.length > 0) {
		throw new Error(
			`${present.join(" and ")} no longer selects an Accounts destination. ` +
				"The frontend reaches Accounts only through auth-gateway/rest: drop " +
				"the variable and compose the frontend with its auth-gateway " +
				"dependency, or set PRODUCT_GATEWAY_INTERNAL to the gateway's REST " +
				"address.",
		);
	}

	const endpoints = options.endpoints ?? getEndpoints();
	const currentModule = options.currentModule ?? getCurrentModule();
	const discovered = endpoints.filter(
		(endpoint) =>
			endpoint.module === currentModule &&
			endpoint.service === "auth-gateway" &&
			endpoint.name === "rest" &&
			endpoint.protocol === "REST",
	);
	if (discovered.length > 1) {
		throw new Error(
			"Codefly returned multiple auth-gateway/rest endpoints; the frontend cannot pick a product API path",
		);
	}
	const gateway =
		discovered[0]?.address ?? environment.PRODUCT_GATEWAY_INTERNAL;
	if (!gateway?.trim()) return undefined;
	return serviceURL(gateway, "Codefly auth-gateway/rest endpoint");
}

function isolatedBindings({ rest, connect }) {
	return Object.freeze({
		rest: serviceURL(rest, "Isolated Accounts REST destination"),
		connect: serviceURL(connect, "Isolated Accounts Connect destination"),
	});
}

/**
 * Server-only product API resolution through the Codefly SDK.
 *
 * The frontend reaches Accounts exclusively through auth-gateway/rest; that one
 * HTTP gateway serves both the REST namespace and the Connect procedures, so
 * every product API call traverses the gateway's route allow-list, rate
 * limiter, and identity-header discipline. Resolution therefore fails closed —
 * a composition without a gateway is a configuration error, never a cue to
 * address Accounts directly.
 *
 * `options.isolatedDirectAccounts` is the only remaining direct path, and it is
 * deliberately a call argument rather than an environment value: no fixture,
 * build mode, or leftover variable can reach it, and the running server passes
 * no arguments at all. Only an in-process test that constructs the object
 * selects it — and such a test covers the frontend in isolation, never the
 * gateway.
 */
export function resolveAccountsBindings(options = {}) {
	if (options.isolatedDirectAccounts) {
		return isolatedBindings(options.isolatedDirectAccounts);
	}
	const gateway = configuredGateway(options);
	if (!gateway) {
		throw new Error(
			"Codefly did not resolve auth-gateway/rest, the frontend's only product " +
				"API path. Run the frontend inside its module graph so the SDK injects " +
				"the dependency, or set PRODUCT_GATEWAY_INTERNAL to the gateway's REST " +
				"address.",
		);
	}
	return Object.freeze({ rest: gateway, connect: gateway });
}

/**
 * Build-time destinations for the Next product API rewrites.
 *
 * Next compiles rewrite destinations into the build manifest, and a container
 * image is built outside the module graph, so an unresolved gateway cannot fail
 * the build. It returns undefined instead: the image then carries no product
 * API rewrite, and the running server rejects that composition — at startup via
 * `instrumentation.ts`, and on the `/api/healthz` readiness probe. What it never
 * does is name a second destination; an ambiguous or malformed gateway still
 * fails the build.
 */
export function resolveProductAPIRewrites(options = {}) {
	const gateway = configuredGateway(options);
	if (!gateway) return undefined;
	return Object.freeze({ rest: gateway, connect: gateway });
}

export function requireAccountsConnect(options = {}) {
	return resolveAccountsBindings(options).connect;
}
