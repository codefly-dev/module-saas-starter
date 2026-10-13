import {
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendServiceAllowlist,
} from "@codefly-dev/saas-plugin-contract";
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

// THE GENERATED ALLOWLIST IS RE-VALIDATED AT LOAD (issue #1121).
//
// It is a build artifact, and this server reads it rather than the composition
// that validated it — so a rule nothing re-checks here is one a stale or
// hand-edited projection walks straight past. The entry is refused as
// `unavailable` rather than `not_installed`, because the alias IS installed and
// what cannot happen is this host serving it.
describe("an entry the composition's own rules would have refused", () => {
	function withEntry(
		overrides: Partial<FrontendServiceAllowlist["entries"][number]>,
	): FrontendServiceAllowlist {
		return {
			...allowlist,
			entries: [{ ...allowlist.entries[0], ...overrides }],
		};
	}

	it.each([
		// `""` is not the root, which is why comparing against the root alone
		// admitted it — and it proxies the whole upstream origin.
		["no route prefix", { routePrefix: "" }],
		["a relative route prefix", { routePrefix: "api/v1" }],
		["a traversal in the prefix", { routePrefix: "/api/../private" }],
		["an empty segment", { routePrefix: "/api//private" }],
		["the origin root on a REST alias", { routePrefix: "/" }],
		[
			"a connect alias with no declared services",
			{ protocol: "connect" as const, routePrefix: "/" },
		],
		[
			"a connect alias whose service list is not a list",
			{
				protocol: "connect" as const,
				routePrefix: "/",
				connectServices:
					"example.records.v1.RecordService" as unknown as readonly string[],
			},
		],
	])("is unavailable: %s", (_label, overrides) => {
		expect(
			resolvePluginServiceFrom(
				withEntry(overrides),
				[endpoint()],
				"example",
				"api",
			),
		).toEqual({ ok: false, reason: "unavailable" });
	});

	it("survives an allowlist whose entries are not a list", () => {
		// A hand-edited or truncated projection: `find` on a non-array throws,
		// and a throw out of the resolver is a 500 where a 404 is the answer.
		expect(
			resolvePluginServiceFrom(
				{ ...allowlist, entries: undefined as unknown as [] },
				[endpoint()],
				"example",
				"api",
			),
		).toEqual({ ok: false, reason: "not_installed" });
	});
});
