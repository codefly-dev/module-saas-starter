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

function accountsEndpoint(endpoints, currentModule, name, protocol) {
	const matches = endpoints.filter(
		(endpoint) =>
			endpoint.module === currentModule &&
			endpoint.service === "accounts" &&
			endpoint.name === name &&
			endpoint.protocol === protocol,
	);
	if (matches.length > 1) {
		throw new Error(`Codefly returned multiple accounts/${name} endpoints`);
	}
	return matches[0]?.address;
}

/** Server-only Accounts binding resolution through the Codefly SDK. */
export function resolveAccountsBindings(options = {}) {
	const endpoints = options.endpoints ?? getEndpoints();
	const currentModule = options.currentModule ?? getCurrentModule();
	const environment = options.environment ?? process.env;
	return Object.freeze({
		rest: optionalServiceURL(
			accountsEndpoint(endpoints, currentModule, "rest", "REST") ??
				environment.API_REST_INTERNAL,
			"Codefly accounts/rest endpoint",
		),
		connect: optionalServiceURL(
			accountsEndpoint(endpoints, currentModule, "connect", "CONNECT") ??
				environment.API_CONNECT_INTERNAL,
			"Codefly accounts/connect endpoint",
		),
	});
}

export function requireAccountsConnect(options = {}) {
	const { connect } = resolveAccountsBindings(options);
	if (!connect)
		throw new Error(
			"Codefly accounts/connect endpoint is required for server-side accounts RPCs",
		);
	return connect;
}
