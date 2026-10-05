// @vitest-environment happy-dom
import { resolveSkinRules } from "@codefly/saas-plugin-contract";
import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { assertSkinRules } from "../src/skin/rules.js";
import * as board from "./board.stories";
import * as composites from "./composites.stories";
import * as content from "./content.stories";
import * as controls from "./controls.stories";
import * as foundations from "./foundations.stories";
import * as metrics from "./metrics.stories";
import * as primitives from "./primitives.stories";
import * as semanticTables from "./semantic-table.stories";
import * as tables from "./table.stories";

afterEach(cleanup);

// Layer 4 is only real if something runs it. Every story is a rendered tree,
// so every story is checked against the strictest rules a shipped example
// skin declares (one page title per page, no skipped heading level). A
// component that violates them here would violate them under that skin.
const HOUSE_RULES = resolveSkinRules({
	slots: { "page-title": { maxPerPage: 1 } },
	headingOrder: "no-skip",
});
for (const [section, stories] of Object.entries({
	controls,
	foundations,
	primitives,
	metrics,
	tables,
	semanticTables,
	composites,
	content,
	board,
})) {
	for (const [name, story] of Object.entries(stories)) {
		if (!("render" in story)) continue;
		it(`${section}/${name} renders its owner implementation`, () => {
			const Story = story.render;
			const { baseElement } = render(<Story />);
			expect(
				baseElement.textContent?.trim() ||
					baseElement.querySelector(
						// A story has to have rendered something. Most prove it with
						// text; these are the primitives whose whole output is
						// non-textual, and each is admitted only in its accessible form —
						// a bar or a spinner with no accessible name has not rendered
						// anything a reader can use, so it should still fail here.
						"input[aria-label], [aria-busy], svg[role=img] path, [role=progressbar][aria-label]",
					),
			).toBeTruthy();
			assertSkinRules(baseElement, HOUSE_RULES);
		});
	}
}
