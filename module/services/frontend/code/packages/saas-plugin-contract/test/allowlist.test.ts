import { describe, expect, it } from "vitest";

import {
	buildFrontendServiceAllowlist,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendServiceBinding,
	type InstalledFrontendService,
} from "../src/index.js";

function service(
	plugin: string,
	alias = "api",
	protocol: "connect" | "rest" = "rest",
): InstalledFrontendService {
	return {
		plugin,
		alias,
		protocol,
		routePrefix: `/api/v1/${plugin}`,
		compatibility: { contract: `${plugin}.api`, major: 1 },
	};
}

function binding(
	plugin: string,
	alias = "api",
	target: FrontendServiceBinding["target"] = {
		module: "products",
		service: plugin,
	},
): FrontendServiceBinding {
	return { plugin, alias, target };
}

describe("frontend plugin service allowlist", () => {
	it("produces an immutable deterministic logical routing inventory", () => {
		const allowlist = buildFrontendServiceAllowlist(
			[service("zeta", "rpc", "connect"), service("alpha")],
			[binding("zeta", "rpc"), binding("alpha")],
		);

		expect(allowlist).toEqual({
			schemaVersion: 1,
			contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
			entries: [
				{
					...service("alpha"),
					target: { module: "products", service: "alpha", endpoint: "rest" },
				},
				{
					...service("zeta", "rpc", "connect"),
					target: { module: "products", service: "zeta", endpoint: "connect" },
				},
			],
		});
		const serialized = JSON.stringify(allowlist);
		expect(serialized).not.toMatch(/https?:|localhost|credential|token/i);
		expect(Object.isFrozen(allowlist)).toBe(true);
		expect(Object.isFrozen(allowlist.entries)).toBe(true);
		expect(Object.isFrozen(allowlist.entries[0]?.target)).toBe(true);
	});

	it("supports a clean starter with no product services", () => {
		expect(buildFrontendServiceAllowlist([], [])).toEqual({
			schemaVersion: 1,
			contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
			entries: [],
		});
	});

	it("fails when an installed requirement has no application binding", () => {
		expect(() =>
			buildFrontendServiceAllowlist([service("example")], []),
		).toThrow("installed service 'example/api' has no application binding");
	});

	it("rejects bindings that are extra or duplicated", () => {
		expect(() =>
			buildFrontendServiceAllowlist([], [binding("example")]),
		).toThrow("does not match an installed service requirement");
		expect(() =>
			buildFrontendServiceAllowlist(
				[service("example")],
				[binding("example"), binding("example")],
			),
		).toThrow("binding 'example/api' is duplicated");
	});

	it.each([
		[
			"raw URL",
			{
				...binding("example"),
				target: {
					module: "products",
					service: "example",
					url: "https://example.invalid",
				},
			} as unknown as FrontendServiceBinding,
			/unknown field 'url'/,
		],
		[
			"unsafe module",
			binding("example", "api", { module: "../private", service: "example" }),
			/module.*unsafe/,
		],
		[
			"unsafe service",
			binding("example", "api", {
				module: "products",
				service: "example/path",
			}),
			/service.*unsafe/,
		],
		[
			"non-Codefly target",
			binding("example", "api", {
				module: "products.internal",
				service: "example",
			}),
			/module.*unsafe/,
		],
		[
			"unknown binding field",
			{
				...binding("example"),
				endpoint: "rest",
			} as unknown as FrontendServiceBinding,
			/unknown field 'endpoint'/,
		],
	])("rejects a binding containing %s", (_case, invalid, diagnostic) => {
		expect(() =>
			buildFrontendServiceAllowlist([service("example")], [invalid]),
		).toThrow(diagnostic);
	});
});
