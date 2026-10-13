import { describe, expect, it } from "vitest";

import {
	TRANSPORT_DEMO_ROUTES,
	transportCarries,
	transportDemoPlugin,
	transportMutates,
} from "./demo-plugin";

describe("the transport playground's demo plugin", () => {
	it("declares a REST prefix and two connect requirements that name their services", () => {
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
				connectServices: ["demo.procedures.v1.RecordService"],
				compatibility: { contract: "demo.procedures", major: 1 },
			},
			{
				alias: "mounted",
				protocol: "connect",
				routePrefix: "/v1/connect",
				connectServices: ["demo.procedures.v1.RecordService"],
				compatibility: { contract: "demo.procedures", major: 1 },
			},
		]);
	});

	it("keeps a procedure outside the declared services on the list, refused", () => {
		// The row that proves the bound exists: with `routePrefix: "/"` the first
		// path segment is the service name, chosen by the caller, so without the
		// declared list this alias would reach every service the upstream origin
		// serves.
		const outside = TRANSPORT_DEMO_ROUTES.find(
			(route) => route.path === "demo.other.v1.SecretService/Read",
		);
		expect(outside?.carried).toBe(false);
		expect(outside && transportCarries(outside)).toBe(false);
	});

	it("confirms every non-GET route, carried or not", () => {
		// The page's plugin name is editable, so a press on a non-read is a real
		// write to whatever backend the operator named — and `carried` answers
		// for the DEMO declaration, not for that plugin's installed requirement
		// (issue #1121, N3). A row the demo reads as refused is carried the
		// moment the named plugin declares that alias differently, and it left
		// the browser unconfirmed. So the arming gate reads the method alone.
		for (const route of TRANSPORT_DEMO_ROUTES) {
			expect(transportMutates(route), route.path).toBe(route.method !== "GET");
		}
		// Non-vacuous in both directions: the table carries reads and writes,
		// and at least one write the demo itself reads as refused.
		expect(
			TRANSPORT_DEMO_ROUTES.filter(transportMutates).length,
		).toBeGreaterThan(0);
		expect(
			TRANSPORT_DEMO_ROUTES.filter((route) => !transportMutates(route)).length,
		).toBeGreaterThan(0);
		expect(
			TRANSPORT_DEMO_ROUTES.filter(
				(route) => !route.carried && transportMutates(route),
			).length,
		).toBeGreaterThan(0);
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
