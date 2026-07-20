import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import { lazy } from "react";
import { describe, expect, it } from "vitest";

import { defineReactFrontend, defineReactPlugin } from "../src/index.js";

const Route = lazy(async () => ({ default: () => null }));
const Widget = lazy(async () => ({ default: () => null }));

const manifest = definePlugin({
	contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
	name: "example",
	routes: [{ id: "overview", path: "/admin/example", requiredRole: "admin" }],
	widgets: [{ id: "example.traffic", slot: "dashboard.widgets", priority: 10 }],
});

describe("public React plugin composition", () => {
	it("joins JSON-safe metadata to React components by stable ID", () => {
		const plugin = defineReactPlugin({
			manifest,
			routes: [{ id: "overview", component: Route }],
			widgets: [{ id: "example.traffic", component: Widget }],
		});
		const config = defineReactFrontend({
			branding: {
				name: "Example",
				mark: "E",
				title: "Example",
				description: "Example",
			},
			plugins: [plugin],
		});

		expect(config.metadata.routes).toEqual([
			{
				plugin: "example",
				id: "overview",
				path: "/admin/example",
				requiredRole: "admin",
			},
		]);
		expect(config.routes[0]?.component).toBe(Route);
		expect(config.widgets[0]?.component).toBe(Widget);
		expect(JSON.parse(JSON.stringify(config.metadata))).toEqual(
			config.metadata,
		);
		expect(Object.isFrozen(config)).toBe(true);
	});

	it("fails closed on missing, duplicate, and extra bindings", () => {
		expect(() => defineReactPlugin({ manifest })).toThrow(
			"plugin 'example' is missing React route 'overview'",
		);
		expect(() =>
			defineReactPlugin({
				manifest,
				routes: [
					{ id: "overview", component: Route },
					{ id: "overview", component: Route },
				],
				widgets: [{ id: "example.traffic", component: Widget }],
			}),
		).toThrow("registers React route 'overview' more than once");
		expect(() =>
			defineReactPlugin({
				manifest,
				routes: [
					{ id: "unknown", component: Route } as unknown as {
						id: "overview";
						component: typeof Route;
					},
				],
				widgets: [{ id: "example.traffic", component: Widget }],
			}),
		).toThrow("registers unknown React route 'unknown'");
	});
});
