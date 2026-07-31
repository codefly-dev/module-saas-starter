// Shared harness for the `pipeline` vitest project — the tier that runs
// against a real Codefly dependency graph.
//
// Every address, secret and origin here comes from the Codefly SDK. Nothing
// reads a CODEFLY__* carrier or assumes an allocated port, so these helpers
// stay correct under an isolated naming scope.

import {
	getCurrentModule,
	getCurrentService,
	getEndpoints,
	resolveServiceAddressSync,
} from "codefly";
import { NextRequest } from "next/server";
import { resolveCodeflyGatewayContext } from "@/lib/codefly-gateway-context";
import { trustedGatewayRequestHeaders } from "@/proxy";

export const INTERNAL_TOKEN_HEADER = "X-Codefly-Internal-Token";
export const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";

/** Naming scope the harness started the graph under, when it started one. */
function testScope(): string {
	return process.env.CODEFLY_TEST_SCOPE ?? "";
}

/**
 * True when Codefly already injected the dependency graph into this process —
 * i.e. we are running under `codefly test service --suite integration`. The
 * global setup uses this to decide whether it must start a graph itself.
 */
export function codeflyInjectedGateway(): boolean {
	const currentModule = getCurrentModule();
	return getEndpoints().some(
		(endpoint) =>
			endpoint.service === "auth-sidecar" &&
			endpoint.name === "rest" &&
			endpoint.protocol === "REST" &&
			(!currentModule || endpoint.module === currentModule) &&
			Boolean(endpoint.address),
	);
}

/**
 * The private product gateway. In a browser this is never addressed directly:
 * Next rewrites the product API namespaces onto it. The pipeline tier runs
 * headless with no Next server, so it addresses the gateway and reproduces the
 * proxy's stamping with the real production libraries below.
 */
export function productGatewayURL(): string {
	const currentModule = getCurrentModule();
	const injected = getEndpoints().filter(
		(endpoint) =>
			endpoint.service === "auth-sidecar" &&
			endpoint.name === "rest" &&
			endpoint.protocol === "REST" &&
			(!currentModule || endpoint.module === currentModule),
	);
	if (injected.length === 1 && injected[0].address) {
		return injected[0].address;
	}
	if (injected.length > 1) {
		throw new Error(
			"Codefly injected multiple auth-sidecar/rest endpoints into the frontend test runtime.",
		);
	}

	// The harness started the graph itself (plain `vitest --project pipeline`).
	// Resolve deterministically within the scope it used.
	const endpoint = resolveServiceAddressSync("auth-sidecar", "rest", {
		scope: testScope(),
		cwd: process.cwd(),
	});
	if (!endpoint) {
		throw new Error(
			"Codefly did not resolve auth-sidecar/rest; run this through " +
				"`codefly test service frontend --suite integration`.",
		);
	}
	return endpoint;
}

/**
 * The frontend's own HTTP origin is the module's public product entry, and the
 * origin the gateway must end up treating as verified. It comes from the SDK,
 * never from a literal or an assumed port.
 */
export function productOrigin(): string {
	const currentModule = getCurrentModule();
	const currentService = getCurrentService();
	const injected = getEndpoints().filter(
		(endpoint) =>
			endpoint.name === "http" &&
			endpoint.protocol === "HTTP" &&
			(!currentModule || endpoint.module === currentModule) &&
			(!currentService || endpoint.service === currentService),
	);
	if (injected.length === 1 && injected[0].address) {
		return injected[0].address;
	}

	const endpoint = resolveServiceAddressSync("frontend", "http", {
		scope: testScope(),
		cwd: process.cwd(),
	});
	if (!endpoint) {
		throw new Error(
			"Codefly did not resolve the frontend/http product origin.",
		);
	}
	return endpoint;
}

/**
 * Reproduce, through the real proxy and gateway-context libraries, the headers
 * a same-origin product API request carries once Next has stamped it. Nothing
 * here hardcodes a token: the internal-auth secret and the public origin both
 * come from the Codefly SDK.
 */
export function stampedGatewayHeaders(): Record<string, string> {
	const origin = productOrigin();
	const context = resolveCodeflyGatewayContext(origin);
	if (!context) {
		throw new Error(
			"Codefly did not provide the frontend gateway context (internal-auth secret and frontend/http endpoint).",
		);
	}
	const stamped = trustedGatewayRequestHeaders(
		new NextRequest(
			new URL("/saas.accounts.v1.OnboardingService/GetProgress", origin),
		),
		context,
	);
	if (!stamped) {
		throw new Error(
			"The frontend proxy did not stamp trust headers on a product API request.",
		);
	}
	const internalToken = stamped.get(INTERNAL_TOKEN_HEADER);
	const publicOrigin = stamped.get(PUBLIC_ORIGIN_HEADER);
	if (!internalToken || !publicOrigin) {
		throw new Error("The frontend proxy stamped an incomplete trust context.");
	}
	return {
		[INTERNAL_TOKEN_HEADER]: internalToken,
		[PUBLIC_ORIGIN_HEADER]: publicOrigin,
	};
}

/** Collision-free suffix so pipeline tests can share one database safely. */
export function uniqueSuffix(): string {
	return crypto.randomUUID().replaceAll("-", "").slice(0, 12);
}
