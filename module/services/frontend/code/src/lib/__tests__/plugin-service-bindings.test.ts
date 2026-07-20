import {
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendServiceAllowlist,
} from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";

import {
	type CodeflyServiceEndpoint,
	codeflyServiceEndpoints,
	resolvePluginServiceFrom,
} from "../../../server/plugin-service-bindings";

const allowlist: FrontendServiceAllowlist = {
	schemaVersion: 1,
	contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
	entries: [
		{
			plugin: "example",
			alias: "api",
			protocol: "rest",
			routePrefix: "/api/v1/example",
			compatibility: { contract: "example.api", major: 1 },
			target: {
				module: "example-module",
				service: "example-api",
				endpoint: "rest",
			},
		},
	],
};

function endpoint(
	address = "http://example-api.internal/",
): CodeflyServiceEndpoint {
	return {
		module: "example-module",
		service: "example-api",
		name: "rest",
		protocol: "REST",
		address,
	};
}

describe("server-only plugin service binding resolution", () => {
	it("reads declared dependency endpoints through the Codefly SDK", () => {
		expect(
			codeflyServiceEndpoints(() => [
				{ ...endpoint("http://example-api.internal"), routes: [] },
			]),
		).toEqual([endpoint("http://example-api.internal")]);
	});

	it("resolves the exact logical Codefly endpoint and normalizes its origin", () => {
		expect(
			resolvePluginServiceFrom(allowlist, [endpoint()], "example", "api"),
		).toEqual({
			ok: true,
			value: {
				entry: allowlist.entries[0],
				baseURL: "http://example-api.internal",
			},
		});
	});

	it("distinguishes an uninstalled alias from an unavailable backend", () => {
		expect(
			resolvePluginServiceFrom(allowlist, [endpoint()], "other", "api"),
		).toEqual({
			ok: false,
			reason: "not_installed",
		});
		expect(resolvePluginServiceFrom(allowlist, [], "example", "api")).toEqual({
			ok: false,
			reason: "unavailable",
		});
	});

	it("fails closed on ambiguous, wrong-protocol, or unsafe destinations", () => {
		expect(
			resolvePluginServiceFrom(
				allowlist,
				[endpoint(), endpoint()],
				"example",
				"api",
			),
		).toEqual({
			ok: false,
			reason: "unavailable",
		});
		expect(
			resolvePluginServiceFrom(
				allowlist,
				[{ ...endpoint(), name: "connect", protocol: "CONNECT" }],
				"example",
				"api",
			),
		).toEqual({ ok: false, reason: "unavailable" });

		for (const address of [
			"ftp://example.internal",
			"https://user:secret@example.internal",
			"https://example.internal/base",
			"https://example.internal?target=other",
			"relative",
		]) {
			expect(
				resolvePluginServiceFrom(
					allowlist,
					[endpoint(address)],
					"example",
					"api",
				),
			).toEqual({
				ok: false,
				reason: "unavailable",
			});
		}
	});
});
