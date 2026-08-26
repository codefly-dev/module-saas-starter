import { describe, expect, it } from "vitest";
import {
	type CodeflyRuntimeReader,
	resolveCodeflyGatewayContext,
} from "@/lib/codefly-gateway-context";

function runtime(
	overrides: Partial<CodeflyRuntimeReader> = {},
): CodeflyRuntimeReader {
	return {
		currentModule: () => "saas-starter",
		currentService: () => "frontend",
		endpoints: () => [
			{
				module: "saas-starter",
				service: "frontend",
				name: "http",
				protocol: "HTTP",
				address: "http://localhost:42152",
				routes: [],
			},
		],
		workspaceSecret: () => "internal-test-token",
		...overrides,
	};
}

describe("Codefly frontend gateway context", () => {
	it("takes the secret from the SDK and, for a loopback placeholder endpoint, the public origin from the request", () => {
		// The render bakes the own HTTP endpoint as a loopback placeholder, so the
		// real public origin must come from the browser request.
		expect(
			resolveCodeflyGatewayContext("https://app.example", runtime()),
		).toEqual({
			internalToken: "internal-test-token",
			publicOrigin: "https://app.example",
		});
	});

	it("prefers a real (non-loopback) SDK endpoint over the request origin", () => {
		expect(
			resolveCodeflyGatewayContext(
				"https://caller-controlled.example",
				runtime({
					endpoints: () => [
						{
							module: "saas-starter",
							service: "frontend",
							name: "http",
							protocol: "HTTP",
							address: "https://app.cell.example",
							routes: [],
						},
					],
				}),
			),
		).toEqual({
			internalToken: "internal-test-token",
			publicOrigin: "https://app.cell.example",
		});
	});

	it("fails closed when a Codefly runtime has no own HTTP endpoint", () => {
		expect(
			resolveCodeflyGatewayContext(
				"https://caller-controlled.example",
				runtime({ endpoints: () => [] }),
			),
		).toBeUndefined();
	});

	it("fails closed on ambiguous or malformed own endpoints", () => {
		const endpoint = runtime().endpoints()[0];
		expect(
			resolveCodeflyGatewayContext(
				"https://caller-controlled.example",
				runtime({ endpoints: () => [endpoint, endpoint] }),
			),
		).toBeUndefined();
		expect(
			resolveCodeflyGatewayContext(
				"https://caller-controlled.example",
				runtime({
					endpoints: () => [
						{ ...endpoint, address: "https://user:secret@app.example" },
					],
				}),
			),
		).toBeUndefined();
	});

	it("fails closed when the workspace secret is unavailable", () => {
		expect(
			resolveCodeflyGatewayContext(
				"https://app.example",
				runtime({ workspaceSecret: () => undefined }),
			),
		).toBeUndefined();
	});

	it("allows request-origin fallback only outside a Codefly runtime", () => {
		expect(
			resolveCodeflyGatewayContext(
				"https://isolated.example",
				runtime({
					currentModule: () => "",
					currentService: () => "",
					endpoints: () => [],
				}),
			),
		).toEqual({
			internalToken: "internal-test-token",
			publicOrigin: "https://isolated.example",
		});
	});
});
