// @vitest-environment happy-dom
import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import * as composites from "./composites.stories";
import * as controls from "./controls.stories";
import * as primitives from "./primitives.stories";
import * as metrics from "./metrics.stories";
import * as tables from "./table.stories";
import * as semanticTables from "./semantic-table.stories";

afterEach(cleanup);
for (const [section, stories] of Object.entries({
	controls,
	primitives,
	metrics,
	tables,
	semanticTables,
	composites,
})) {
	for (const [name, story] of Object.entries(stories)) {
		if (!("render" in story)) continue;
		it(`${section}/${name} renders its owner implementation`, () => {
			const Story = story.render;
			const { baseElement } = render(<Story />);
			expect(
				baseElement.textContent?.trim() ||
					baseElement.querySelector(
						"input[aria-label], [aria-busy], svg[role=img] path",
					),
			).toBeTruthy();
		});
	}
}
