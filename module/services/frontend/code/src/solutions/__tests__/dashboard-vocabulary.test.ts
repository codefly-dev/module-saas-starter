import type {
	DashboardLayout as ManifestLayout,
	DataGraph,
	WidgetVisualization as ManifestVisualization,
} from "@codefly/saas-plugin-manifest";
import { assertDataGraph } from "@codefly/saas-plugin-manifest";
import type {
	DashboardLayout as SdkLayout,
	WidgetVisualization as SdkVisualization,
} from "@codefly-dev/saas-sdk";
import {
	DASHBOARD_DATA_SCHEMA,
	DASHBOARD_VISUALIZATIONS,
	type DashboardLayoutKind as KitLayout,
	type WidgetVisualization as KitVisualization,
} from "@codefly-dev/ui/dashboard";
import { describe, expect, it } from "vitest";

// The three places the chart vocabulary is written down, held to one list.
//
// The kit publishes it — `@codefly-dev/ui/dashboard-catalog.json` and the data
// model beside it — and `@codefly-dev/saas-sdk` (the data runtime) and
// `@codefly/saas-plugin-manifest` (what a solution may declare) each restate it
// in their own types. This is where all three are importable, because this is
// where they meet: `SolutionDashboard` resolves a declared graph through the SDK
// and renders it with the kit.
//
// Two halves, and both are needed. The assignments below are checked by the
// host's `tsc`, which covers `**/*.ts` including this file, so a union that gains
// a member in one package and not the others fails to typecheck. The runtime
// assertions then drive the manifest's own validator, because a type agreement
// says nothing about the list a validator actually enforces.

// A declaration using one visualization, in the manifest's own shape.
function graphWith(visualization: string): unknown {
	const graph: DataGraph = {
		events: [{ name: "item_created", type: "acme.item.created.v1" }],
		metrics: [
			{
				id: "items_over_time",
				kind: "source",
				filter: { event: "item_created" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
		],
		dashboards: [
			{
				id: "overview",
				layout: "grid",
				widgets: [
					{
						id: "trend",
						metric: "items_over_time",
						visualization: visualization as ManifestVisualization,
					},
				],
			},
		],
	};
	return graph;
}

describe("the published catalog is the one chart vocabulary", () => {
	it("is one union across the kit, the SDK and the manifest package", () => {
		// A ring of assignments: each arrow is one direction of assignability, so
		// kit ⊆ SDK ⊆ manifest ⊆ kit means the three unions are the same set.
		// Checked by the host's `tsc`, which covers this file; a member added to
		// one package alone breaks the ring.
		const kit: KitVisualization = "line";
		const sdk: SdkVisualization = kit;
		const manifest: ManifestVisualization = sdk;
		const back: KitVisualization = manifest;
		expect(back).toBe("line");

		const kitLayout: KitLayout = "grid";
		const sdkLayout: SdkLayout = kitLayout;
		const manifestLayout: ManifestLayout = sdkLayout;
		const backLayout: KitLayout = manifestLayout;
		expect(backLayout).toBe("grid");
	});

	it("is a list the published document and the kit agree on", () => {
		const published = DASHBOARD_DATA_SCHEMA.$defs.DashboardWidgetView.properties
			.visualization.enum as readonly string[];
		expect([...published]).toEqual([...DASHBOARD_VISUALIZATIONS]);
	});

	it("names only visualizations a solution may declare", () => {
		for (const visualization of DASHBOARD_VISUALIZATIONS) {
			expect(
				() => assertDataGraph(graphWith(visualization)),
				`the manifest rejects '${visualization}', which the kit publishes`,
			).not.toThrow();
		}
	});

	it("names every visualization a solution may declare", () => {
		// The other direction, and the one a type cannot state: the manifest's
		// validator holds its own list, so a member added there and not here
		// would be declarable and unpublished. Driving a visualization the
		// catalog does not name proves the validator is the narrower gate.
		expect(() => assertDataGraph(graphWith("pie"))).toThrow(
			/visualization 'pie' is unsupported/,
		);
	});
});
