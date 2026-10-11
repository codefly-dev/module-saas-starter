// @vitest-environment node
import { readFileSync } from "node:fs";
import Ajv2020 from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import fixture from "../../../src/solutions/__tests__/fixtures/platform-signins.json";
import { assertDataGraph } from "../src/data-graph.js";

const schema = JSON.parse(
	readFileSync(
		new URL("../plugin.codefly.schema.json", import.meta.url),
		"utf8",
	),
);
const validate = new Ajv2020({ strict: false }).compile({
	$defs: schema.$defs,
	$ref: "#/$defs/dashboard",
});
function graph() {
	const result = structuredClone(fixture);
	return {
		...result,
		dashboards: result.dashboards.map((d) => ({
			...d,
			columns: 3,
			sections: [{ id: "summary", title: "Summary", columns: 4 }],
			widgets: d.widgets.map((w) => ({ ...w, span: 2, section: "summary" })),
		})),
	};
}

describe("additive layout declarations", () => {
	it("accepts the unchanged nine-widget graph and a sectioned graph in both validators", () => {
		for (const g of [fixture, graph()]) {
			expect(() => assertDataGraph(g)).not.toThrow();
			expect(validate(g), JSON.stringify(validate.errors)).toBe(true);
		}
	});
	it.each([0, 5, 1.5, "2", null])(
		"rejects invalid column counts and spans: %s",
		(value) => {
			for (const target of ["dashboard", "section", "widget"]) {
				const g = graph();
				const d = g.dashboards[0];
				Object.assign(
					target === "dashboard"
						? d
						: target === "section"
							? d.sections[0]
							: d.widgets[0],
					{ [target === "widget" ? "span" : "columns"]: value },
				);
				expect(() => assertDataGraph(g)).toThrow();
				expect(validate(g)).toBe(false);
			}
		},
	);
	it("rejects duplicate and missing section references", () => {
		const duplicate = graph();
		duplicate.dashboards[0].sections.push(duplicate.dashboards[0].sections[0]);
		expect(() => assertDataGraph(duplicate)).toThrow(/section id/);
		const missing = graph();
		missing.dashboards[0].widgets[0].section = "absent";
		expect(() => assertDataGraph(missing)).toThrow(/unknown section/);
	});
	it("permits ungrouped widgets alongside named sections", () => {
		const g = graph();
		Reflect.deleteProperty(g.dashboards[0].widgets[0], "section");
		expect(() => assertDataGraph(g)).not.toThrow();
	});
});

it("ignores future presentation hints while retaining strict metric validation", () => {
	const g = graph();
	Object.assign(g.dashboards[0], { futureLayout: "compact" });
	Object.assign(g.dashboards[0].sections[0], { futureHeading: true });
	Object.assign(g.dashboards[0].widgets[0], { futureSize: 2 });
	expect(() => assertDataGraph(g)).not.toThrow();
	expect(validate(g)).toBe(true);
	Object.assign(g.metrics[0], { futureQuery: "unsafe" });
	expect(() => assertDataGraph(g)).toThrow(/unknown field/);
	expect(validate(g)).toBe(false);
});
