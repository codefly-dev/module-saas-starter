import { getCurrentModule, getEndpoints } from "codefly";

function optionalServiceURL(value, variable) {
	if (!value) return undefined;
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

function serviceEndpoint(
	endpoints,
	currentModule,
	service,
	name,
	protocol,
) {
	const matches = endpoints.filter(
		(endpoint) =>
			endpoint.module === currentModule &&
			endpoint.service === service &&
			endpoint.name === name &&
			endpoint.protocol === protocol,
	);
	if (matches.length > 1) {
		throw new Error(
			`Codefly returned multiple ${service}/${name} endpoints`,
		);
	}
	return matches[0]?.address;
}

/**
 * Server-only product API resolution through the Codefly SDK.
 *
 * In a complete module flow, the frontend reaches Accounts exclusively through
 * auth-gateway/rest. Both REST and Connect are served by that exact HTTP
 * gateway. Direct Accounts bindings remain only as an explicit fallback for
 * isolated frontend/Playwright tests that do not start the module graph.
 */
export function resolveAccountsBindings(options = {}) {
	const endpoints = options.endpoints ?? getEndpoints();
	const currentModule = options.currentModule ?? getCurrentModule();
	const environment = options.environment ?? process.env;
	const gateway =
		serviceEndpoint(
			endpoints,
			currentModule,
			"auth-gateway",
			"rest",
			"REST",
		) ?? environment.PRODUCT_GATEWAY_INTERNAL;
	if (gateway) {
		const normalized = optionalServiceURL(
			gateway,
			"Codefly auth-gateway/rest endpoint",
		);
		return Object.freeze({ rest: normalized, connect: normalized });
	}
	return Object.freeze({
		rest: optionalServiceURL(
			serviceEndpoint(
				endpoints,
				currentModule,
				"accounts",
				"rest",
				"REST",
			) ??
				environment.API_REST_INTERNAL,
			"Codefly accounts/rest endpoint",
		),
		connect: optionalServiceURL(
			serviceEndpoint(
				endpoints,
				currentModule,
				"accounts",
				"connect",
				"CONNECT",
			) ??
				environment.API_CONNECT_INTERNAL,
			"Codefly accounts/connect endpoint",
		),
	});
}

export function requireAccountsConnect(options = {}) {
	const { connect } = resolveAccountsBindings(options);
	if (!connect)
		throw new Error(
			"Codefly product API gateway is required for server-side Accounts RPCs",
		);
	return connect;
}
