import type { ServiceEndpoint } from "codefly";
import { describe, expect, it } from "vitest";

import {
	codeflyInjectedRuntime,
	type PipelineRuntimeReader,
	productGatewayURL,
	productOrigin,
} from "@/test/pipeline-gateway";

const ORIGIN = "http://localhost:21931";
const GATEWAY = "http://localhost:42152";

function frontendHTTP(
	overrides: Partial<ServiceEndpoint> = {},
): ServiceEndpoint {
	return {
		module: "saas-starter",
		service: "frontend",
		name: "http",
		protocol: "HTTP",
		address: ORIGIN,
		routes: [],
		...overrides,
	};
}

function authGatewayREST(
	overrides: Partial<ServiceEndpoint> = {},
): ServiceEndpoint {
	return frontendHTTP({
		service: "auth-gateway",
		name: "rest",
		protocol: "REST",
		address: GATEWAY,
		...overrides,
	});
}

function runtime(
	overrides: Partial<PipelineRuntimeReader> = {},
): PipelineRuntimeReader {
	return {
		currentModule: () => "saas-starter",
		currentService: () => "frontend",
		endpoints: () => [],
		resolveAddress: () => null,
		...overrides,
	};
}

/** A harness that started the graph itself: Codefly owns no execution context. */
function selfStartedRuntime(
	overrides: Partial<PipelineRuntimeReader> = {},
): PipelineRuntimeReader {
	return runtime({
		currentModule: () => "",
		currentService: () => "",
		...overrides,
	});
}

describe("codeflyInjectedRuntime", () => {
	it("recognizes a Codefly-owned test before endpoints are injected", () => {
		expect(codeflyInjectedRuntime(runtime({ endpoints: () => [] }))).toBe(true);
	});

	it("leaves a plain Vitest process responsible for starting dependencies", () => {
		expect(
			codeflyInjectedRuntime(
				runtime({ currentModule: () => "", currentService: () => "" }),
			),
		).toBe(false);
	});
});

describe("productOrigin", () => {
	it("returns the frontend's own injected HTTP origin", () => {
		expect(productOrigin(runtime({ endpoints: () => [frontendHTTP()] }))).toBe(
			ORIGIN,
		);
	});

	it("fails closed when Codefly injects more than one frontend/http endpoint", () => {
		// Two candidates means this is not the frontend's own graph. Even with a
		// resolvable fallback available, silently picking one would address the
		// wrong service — so it must throw rather than guess. This is the guard
		// productGatewayURL already had and productOrigin was missing.
		const ambiguous = runtime({
			endpoints: () => [
				frontendHTTP({ address: ORIGIN }),
				frontendHTTP({ address: "http://localhost:30000" }),
			],
			resolveAddress: () => "http://localhost:9999",
		});
		expect(() => productOrigin(ambiguous)).toThrow(
			/multiple frontend\/http endpoints/i,
		);
	});

	it("ignores endpoints from other services when selecting the origin", () => {
		const withNoise = runtime({
			endpoints: () => [
				authGatewayREST(),
				frontendHTTP({
					service: "marketing",
					address: "http://localhost:38311",
				}),
				frontendHTTP(),
			],
		});
		expect(productOrigin(withNoise)).toBe(ORIGIN);
	});

	it("falls back to SDK resolution when the harness started the graph itself", () => {
		expect(
			productOrigin(
				selfStartedRuntime({
					endpoints: () => [],
					resolveAddress: () => ORIGIN,
				}),
			),
		).toBe(ORIGIN);
	});

	it("throws when the origin can be neither injected nor resolved", () => {
		expect(() => productOrigin(selfStartedRuntime())).toThrow(
			/did not resolve frontend\/http/i,
		);
	});
});

describe("productGatewayURL", () => {
	it("returns the single injected auth-gateway/rest address", () => {
		expect(
			productGatewayURL(runtime({ endpoints: () => [authGatewayREST()] })),
		).toBe(GATEWAY);
	});

	it("fails closed when Codefly injects more than one auth-gateway/rest endpoint", () => {
		const ambiguous = runtime({
			endpoints: () => [
				authGatewayREST({ address: GATEWAY }),
				authGatewayREST({ address: "http://localhost:30001" }),
			],
			resolveAddress: () => "http://localhost:9999",
		});
		expect(() => productGatewayURL(ambiguous)).toThrow(
			/multiple auth-gateway\/rest endpoints/i,
		);
	});

	it("falls back to SDK resolution when the harness started the graph itself", () => {
		expect(
			productGatewayURL(
				selfStartedRuntime({
					endpoints: () => [],
					resolveAddress: () => GATEWAY,
				}),
			),
		).toBe(GATEWAY);
	});

	it("refuses a default-scope fallback while Codefly owns the process", () => {
		// Codefly owns this process (module + service are set) but has not
		// injected the endpoint yet. The deterministic lookup answers for the
		// DEFAULT naming scope, not the scope Codefly actually started, so
		// accepting it would hand the harness a well-formed address for a graph
		// that is not under test — the Playwright web server would then proxy the
		// product API to the wrong gateway and the suite would prove nothing while
		// looking healthy. It must refuse instead.
		const notYetInjected = runtime({
			endpoints: () => [],
			resolveAddress: () => "http://localhost:9999",
		});
		expect(() => productGatewayURL(notYetInjected)).toThrow(
			/owns this process but injected no auth-gateway\/rest/i,
		);
		expect(() => productOrigin(notYetInjected)).toThrow(
			/owns this process but injected no frontend\/http/i,
		);
	});
});
