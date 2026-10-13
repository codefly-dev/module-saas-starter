import { describe, expect, it, vi } from "vitest";
import {
	type CodeflyRuntimeReader,
	resolveCodeflyGatewayContext,
} from "@/lib/codefly-gateway-context";

const loopbackEndpoint = {
	module: "saas-starter",
	service: "frontend",
	name: "http",
	protocol: "HTTP" as const,
	address: "http://localhost:42152",
	routes: [],
};

/** A deployed runtime whose own endpoint is the render's loopback placeholder. */
function runtime(
	overrides: Partial<CodeflyRuntimeReader> = {},
): CodeflyRuntimeReader {
	return {
		currentModule: () => "saas-starter",
		currentService: () => "frontend",
		endpoints: () => [loopbackEndpoint],
		workspaceSecret: () => "internal-test-token",
		workspaceConfiguration: () => undefined,
		isDeployedBuild: () => true,
		...overrides,
	};
}

function configured(value: string | undefined): Partial<CodeflyRuntimeReader> {
	return {
		workspaceConfiguration: (group, key) =>
			group === "application" && key === "APP_BASE_URL" ? value : undefined,
	};
}

describe("Codefly frontend gateway context", () => {
	it("takes the public origin from operator configuration", () => {
		expect(
			resolveCodeflyGatewayContext(
				runtime(configured("https://app.cell.example")),
			),
		).toEqual({
			internalToken: "internal-test-token",
			publicOrigin: "https://app.cell.example",
		});
	});

	// The defect: the render bakes the own endpoint as a loopback placeholder, so
	// this fell back to the caller's X-Forwarded-Host and stamped it, under the
	// internal token, as the VERIFIED public origin. accounts then bound an OAuth
	// redirect, the authenticator relying-party origin and emailed links to a host
	// the caller chose. There is no request to fall back to any more — the function
	// does not take one.
	it("refuses, by name, when a deployed build has no configured origin and a loopback placeholder endpoint", () => {
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		try {
			expect(resolveCodeflyGatewayContext(runtime())).toBeUndefined();
			expect(error).toHaveBeenCalledWith(
				expect.stringContaining("application/APP_BASE_URL"),
			);
		} finally {
			error.mockRestore();
		}
	});

	it("uses a render that carries a real ingress host when nothing is pinned", () => {
		expect(
			resolveCodeflyGatewayContext(
				runtime({
					endpoints: () => [
						{ ...loopbackEndpoint, address: "https://app.cell.example" },
					],
				}),
			),
		).toEqual({
			internalToken: "internal-test-token",
			publicOrigin: "https://app.cell.example",
		});
	});

	it("keeps the loopback endpoint in local development, where the browser reaches it", () => {
		expect(
			resolveCodeflyGatewayContext(runtime({ isDeployedBuild: () => false })),
		).toEqual({
			internalToken: "internal-test-token",
			publicOrigin: "http://localhost:42152",
		});
	});

	it("prefers the configured origin over a render that carries a real host", () => {
		expect(
			resolveCodeflyGatewayContext(
				runtime({
					...configured("https://pinned.example"),
					endpoints: () => [
						{ ...loopbackEndpoint, address: "https://rendered.example" },
					],
				}),
			)?.publicOrigin,
		).toBe("https://pinned.example");
	});

	it("refuses a configured value that is not an exact origin", () => {
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		try {
			for (const value of [
				"https://app.example/path",
				"https://user:secret@app.example",
				"app.example",
				"ftp://app.example",
				"https://app.example?q=1",
			]) {
				expect(
					resolveCodeflyGatewayContext(runtime(configured(value))),
				).toBeUndefined();
			}
			expect(error).toHaveBeenCalled();
		} finally {
			error.mockRestore();
		}
	});

	it("fails closed when a Codefly runtime has no own HTTP endpoint and nothing is configured", () => {
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		try {
			expect(
				resolveCodeflyGatewayContext(runtime({ endpoints: () => [] })),
			).toBeUndefined();
		} finally {
			error.mockRestore();
		}
	});

	it("fails closed on ambiguous or malformed own endpoints", () => {
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		try {
			expect(
				resolveCodeflyGatewayContext(
					runtime({
						isDeployedBuild: () => false,
						endpoints: () => [loopbackEndpoint, loopbackEndpoint],
					}),
				),
			).toBeUndefined();
			expect(
				resolveCodeflyGatewayContext(
					runtime({
						isDeployedBuild: () => false,
						endpoints: () => [
							{
								...loopbackEndpoint,
								address: "https://user:secret@app.example",
							},
						],
					}),
				),
			).toBeUndefined();
		} finally {
			error.mockRestore();
		}
	});

	it("fails closed when the workspace secret is unavailable", () => {
		expect(
			resolveCodeflyGatewayContext(
				runtime({
					...configured("https://app.cell.example"),
					workspaceSecret: () => undefined,
				}),
			),
		).toBeUndefined();
	});

	// Outside a Codefly runtime (an isolated component test) there is no own
	// endpoint and no configuration, so there is no origin — and, with no request
	// to borrow one from, nothing to guess with.
	it("fails closed outside a Codefly runtime unless an origin is configured", () => {
		const outside = {
			currentModule: () => "",
			currentService: () => "",
			endpoints: () => [],
			isDeployedBuild: () => false,
		};
		expect(resolveCodeflyGatewayContext(runtime(outside))).toBeUndefined();
		expect(
			resolveCodeflyGatewayContext(
				runtime({ ...outside, ...configured("https://isolated.example") }),
			)?.publicOrigin,
		).toBe("https://isolated.example");
	});
});
