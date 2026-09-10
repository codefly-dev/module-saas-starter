// The one rule for resolving a Codefly endpoint from a test process, shared by
// every harness that needs one: the pipeline vitest tier, the Playwright config,
// and the e2e global setup.
//
// Every address comes from the Codefly SDK. Nothing reads a CODEFLY__* carrier
// or assumes an allocated port, so these helpers stay correct under an isolated
// naming scope. Keeping one copy is the point: three harnesses each carrying
// their own version of the ambiguity guard and the scope-aware fallback is how
// one of them ends up silently addressing the wrong graph.

import {
	type EndpointProtocol,
	getCurrentModule,
	getCurrentService,
	getEndpoints,
	resolveServiceAddressSync,
	type ServiceEndpoint,
} from "codefly";

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
 * Resolve the single endpoint a harness must address. Under `codefly test
 * service` Codefly injects the endpoints into this process; a harness that
 * started the graph itself resolves them from the SDK within the scope it used.
 * Either way exactly one match is legal: more than one injected candidate means
 * this is not the frontend's own graph, so it fails closed rather than silently
 * addressing the wrong service.
 */
function resolveCodeflyEndpoint(
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

	// Codefly owns this process, so the graph it started is the only correct
	// answer and the injected set is the only place that names it. The
	// deterministic lookup below answers for the DEFAULT naming scope, which is
	// not the scope Codefly started — it would hand back a well-formed address
	// for a graph that is not under test, and the harness would prove nothing
	// while looking healthy. Refuse instead.
	if (codeflyInjectedRuntime(runtime)) {
		throw new Error(
			`Codefly owns this process but injected no ${label} endpoint. ` +
				"Refusing to fall back to a default-scope address that would " +
				"address a different graph than the one under test.",
		);
	}

	// The harness started the graph itself. Resolve deterministically within the
	// scope it used.
	const endpoint = runtime.resolveAddress(fallback.service, fallback.apiType);
	if (!endpoint) {
		throw new Error(
			`Codefly did not resolve ${label}; run this through ` +
				"`codefly test service frontend`.",
		);
	}
	return endpoint;
}

/**
 * The private product gateway — the frontend's single product API path. A
 * browser never addresses it directly: `src/proxy.ts` forwards the product API
 * namespaces onto it. Accounts is never a destination here; a direct-backend
 * run would prove nothing about the gateway's route allow-list, limiter, or
 * identity headers.
 */
export function productGatewayURL(
	runtime: PipelineRuntimeReader = runtimeSDK,
): string {
	const currentModule = runtime.currentModule();
	return resolveCodeflyEndpoint(
		"auth-gateway/rest",
		{ service: "auth-gateway", apiType: "rest" },
		(endpoint) =>
			endpoint.service === "auth-gateway" &&
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
	return resolveCodeflyEndpoint(
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
