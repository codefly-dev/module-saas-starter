// Shared harness for the `pipeline` vitest project — the tier that runs
// against a real Codefly dependency graph.
//
// Every address, secret and origin here comes from the Codefly SDK. Nothing
// reads a CODEFLY__* carrier or assumes an allocated port, so these helpers
// stay correct under an isolated naming scope.

import {
	type EndpointProtocol,
	getCurrentModule,
	getCurrentService,
	getEndpoints,
	resolveServiceAddressSync,
	type ServiceEndpoint,
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
 * The Codefly runtime values the endpoint resolvers below depend on. Injected
 * with `runtimeSDK` in production so the callers stay argument-free, but
 * overridable in a unit test — the same seam `CodeflyRuntimeReader` gives the
 * frontend gateway context — so the fail-closed behavior can be exercised
 * without a live dependency graph.
 */
export interface PipelineRuntimeReader {
	currentModule(): string;
	currentService(): string;
	endpoints(): ServiceEndpoint[];
	resolveAddress(service: string, apiType: EndpointProtocol): string | null;
}

const runtimeSDK: PipelineRuntimeReader = {
	currentModule: getCurrentModule,
	currentService: getCurrentService,
	endpoints: getEndpoints,
	resolveAddress: (service, apiType) =>
		resolveServiceAddressSync(service, apiType, {
			scope: testScope(),
			cwd: process.cwd(),
		}),
};

/**
 * True when Codefly owns this test process and has already started its service
 * dependencies. Endpoint injection can occur after Vitest global setup, so the
 * execution context—not the current endpoint snapshot—is the stable boundary.
 */
export function codeflyInjectedRuntime(
	runtime: Pick<
		PipelineRuntimeReader,
		"currentModule" | "currentService"
	> = runtimeSDK,
): boolean {
	return Boolean(runtime.currentModule() && runtime.currentService());
}

/**
 * Resolve the single endpoint the pipeline harness must address. Under `codefly
 * test service` Codefly injects the endpoints into this process; a plain
 * `vitest --project pipeline` run resolves them from the SDK within the scope
 * the harness started the graph under. Either way exactly one match is legal:
 * more than one injected candidate means this is not the frontend's own graph,
 * so it fails closed rather than silently addressing the wrong service. Both
 * resolvers share this so that guard cannot drift between them.
 */
function resolvePipelineEndpoint(
	label: string,
	fallback: { service: string; apiType: EndpointProtocol },
	matches: (endpoint: ServiceEndpoint) => boolean,
	runtime: PipelineRuntimeReader,
): string {
	const injected = runtime.endpoints().filter(matches);
	if (injected.length > 1) {
		throw new Error(
			`Codefly injected multiple ${label} endpoints into the frontend test runtime.`,
		);
	}
	if (injected.length === 1 && injected[0].address) {
		return injected[0].address;
	}

	// The harness started the graph itself (plain `vitest --project pipeline`).
	// Resolve deterministically within the scope it used.
	const endpoint = runtime.resolveAddress(fallback.service, fallback.apiType);
	if (!endpoint) {
		throw new Error(
			`Codefly did not resolve ${label}; run this through ` +
				"`codefly test service frontend --suite integration`.",
		);
	}
	return endpoint;
}

/**
 * The private product gateway. In a browser this is never addressed directly:
 * Next rewrites the product API namespaces onto it. The pipeline tier runs
 * headless with no Next server, so it addresses the gateway and reproduces the
 * proxy's stamping with the real production libraries below.
 */
export function productGatewayURL(
	runtime: PipelineRuntimeReader = runtimeSDK,
): string {
	const currentModule = runtime.currentModule();
	return resolvePipelineEndpoint(
		"auth-sidecar/rest",
		{ service: "auth-sidecar", apiType: "rest" },
		(endpoint) =>
			endpoint.service === "auth-sidecar" &&
			endpoint.name === "rest" &&
			endpoint.protocol === "REST" &&
			(!currentModule || endpoint.module === currentModule),
		runtime,
	);
}

/**
 * The frontend's own HTTP origin is the module's public product entry, and the
 * origin the gateway must end up treating as verified. It comes from the SDK,
 * never from a literal or an assumed port.
 */
export function productOrigin(
	runtime: PipelineRuntimeReader = runtimeSDK,
): string {
	const currentModule = runtime.currentModule();
	const currentService = runtime.currentService();
	return resolvePipelineEndpoint(
		"frontend/http",
		{ service: "frontend", apiType: "http" },
		(endpoint) =>
			endpoint.name === "http" &&
			endpoint.protocol === "HTTP" &&
			(!currentModule || endpoint.module === currentModule) &&
			(!currentService || endpoint.service === currentService),
		runtime,
	);
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
