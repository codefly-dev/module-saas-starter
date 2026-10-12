import { describe, expect, it } from "vitest";

import {
	TRANSPORT_DEMO_ROUTES,
	transportCarries,
	transportDemoPlugin,
} from "./demo-plugin";

describe("the transport playground's demo plugin", () => {
	it("declares one REST prefix and one connect requirement at the origin root", () => {
		expect(transportDemoPlugin.name).toBe("transport-demo");
		expect(transportDemoPlugin.services).toEqual([
			{
				alias: "records",
				protocol: "rest",
				routePrefix: "/v1/demo",
				compatibility: { contract: "demo.records", major: 1 },
			},
			{
				alias: "procedures",
				protocol: "connect",
				routePrefix: "/",
				compatibility: { contract: "demo.procedures", major: 1 },
			},
		]);
	});

	it("names every route against a declared alias", () => {
		const aliases = new Set(
			(transportDemoPlugin.services ?? []).map((service) => service.alias),
		);
		for (const route of TRANSPORT_DEMO_ROUTES) {
			expect(aliases, route.path).toContain(route.alias);
		}
	});

	it("lists the four custom-verb shapes a module's bindings produce", () => {
		expect(
			TRANSPORT_DEMO_ROUTES.filter(
				(route) => route.carried && route.path.includes(":"),
			).map((route) => route.path),
		).toEqual([
			"records:recover",
			"records/record-1/commands:recover",
			"records/record-1/interactions/interaction-1:respond",
			"triggers/nightly:fire",
		]);
	});

	// The row's own flag is editorial; the transport's answer is the fact. If
	// they ever disagree, the table is describing a transport nobody has.
	it.each(TRANSPORT_DEMO_ROUTES.map((route) => [route.path, route]))(
		"agrees with the transport about '%s'",
		(_path, route) => {
			expect(transportCarries(route)).toBe(route.carried);
		},
	);

	it("keeps at least one refused shape on the list", () => {
		expect(
			TRANSPORT_DEMO_ROUTES.filter((route) => !route.carried).length,
		).toBeGreaterThan(0);
	});
});
