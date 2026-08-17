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

function authSidecarREST(
	overrides: Partial<ServiceEndpoint> = {},
): ServiceEndpoint {
	return frontendHTTP({
		service: "auth-sidecar",
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
				authSidecarREST(),
				frontendHTTP({
					service: "marketing",
					address: "http://localhost:38311",
				}),
				frontendHTTP(),
			],
		});
		expect(productOrigin(withNoise)).toBe(ORIGIN);
	});

	it("falls back to SDK resolution when nothing is injected", () => {
		expect(
			productOrigin(
				runtime({ endpoints: () => [], resolveAddress: () => ORIGIN }),
			),
		).toBe(ORIGIN);
	});

	it("throws when the origin can be neither injected nor resolved", () => {
		expect(() => productOrigin(runtime())).toThrow(
			/did not resolve frontend\/http/i,
		);
	});
});

describe("productGatewayURL", () => {
	it("returns the single injected auth-sidecar/rest address", () => {
		expect(
			productGatewayURL(runtime({ endpoints: () => [authSidecarREST()] })),
		).toBe(GATEWAY);
	});

	it("fails closed when Codefly injects more than one auth-sidecar/rest endpoint", () => {
		const ambiguous = runtime({
			endpoints: () => [
				authSidecarREST({ address: GATEWAY }),
				authSidecarREST({ address: "http://localhost:30001" }),
			],
			resolveAddress: () => "http://localhost:9999",
		});
		expect(() => productGatewayURL(ambiguous)).toThrow(
			/multiple auth-sidecar\/rest endpoints/i,
		);
	});

	it("falls back to SDK resolution when nothing is injected", () => {
		expect(
			productGatewayURL(
				runtime({ endpoints: () => [], resolveAddress: () => GATEWAY }),
			),
		).toBe(GATEWAY);
	});
});
