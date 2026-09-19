// @vitest-environment happy-dom
import { resolveSkinRules } from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";

import { assertSkinRules, checkSkinRules } from "../rules.js";

// Layer 4 is skin data a CHECKER reads, never CSS. These prove the two halves:
// the contract refuses a rule it cannot enforce, and the checker reads rendered
// markup through the same `data-slot` vocabulary layer 3 points at.

function tree(html: string): ParentNode {
	const container = document.createElement("div");
	container.innerHTML = html;
	return container;
}

describe("the rules schema is fail-closed like the rest of the contract", () => {
	it("accepts a rule it can enforce", () => {
		const rules = resolveSkinRules({
			slots: { "page-title": { maxPerPage: 1 } },
			headingOrder: "no-skip",
		});
		expect(rules.slots?.["page-title"]?.maxPerPage).toBe(1);
		expect(rules.headingOrder).toBe("no-skip");
	});

	it("defaults to constraining nothing", () => {
		const rules = resolveSkinRules(undefined);
		expect(rules.headingOrder).toBe("any");
		expect(Object.keys(rules.slots ?? {})).toEqual([]);
	});

	it.each([
		[
			{ slots: { "hero-title": { maxPerPage: 1 } } },
			/unknown field 'hero-title'/,
		],
		[
			{ slots: { "page-title": { maxPerRoute: 1 } } },
			/unknown field 'maxPerRoute'/,
		],
		[{ slots: { "page-title": { maxPerPage: -1 } } }, /whole number/],
		[{ slots: { "page-title": { maxPerPage: 1.5 } } }, /whole number/],
		[{ headingOrder: "strict" }, /headingOrder 'strict' is unsupported/],
		[{ allowedSurfaces: ["marketing"] }, /unknown field 'allowedSurfaces'/],
	])("refuses %j", (bad, message) => {
		expect(() => resolveSkinRules(bad)).toThrow(message);
	});
});

describe("checkSkinRules reads a rendered tree", () => {
	it("passes a page inside its budget", () => {
		const rules = resolveSkinRules({
			slots: { "page-title": { maxPerPage: 1 } },
		});
		expect(
			checkSkinRules(tree('<h1 data-slot="page-title">One</h1>'), rules),
		).toEqual([]);
	});

	it("reports a slot used more often than the skin allows", () => {
		const rules = resolveSkinRules({
			slots: { "page-title": { maxPerPage: 1 } },
		});
		const violations = checkSkinRules(
			tree(
				'<h1 data-slot="page-title">One</h1><h1 data-slot="page-title">Two</h1>',
			),
			rules,
		);
		expect(violations).toHaveLength(1);
		expect(violations[0].rule).toBe("maxPerPage");
		expect(violations[0].message).toContain("appears 2 times");
	});

	it("reports a heading level that jumps", () => {
		const rules = resolveSkinRules({ headingOrder: "no-skip" });
		const violations = checkSkinRules(tree("<h2>A</h2><h4>B</h4>"), rules);
		expect(violations).toHaveLength(1);
		expect(violations[0].message).toContain("h2 to h4");
	});

	it("allows a heading level to step back out", () => {
		const rules = resolveSkinRules({ headingOrder: "no-skip" });
		expect(
			checkSkinRules(tree("<h1>A</h1><h2>B</h2><h3>C</h3><h2>D</h2>"), rules),
		).toEqual([]);
	});

	// A fragment rendered on its own legitimately starts deeper than h1, so the
	// first heading sets the level rather than being required to be h1.
	it("lets a fragment start at any level", () => {
		const rules = resolveSkinRules({ headingOrder: "no-skip" });
		expect(checkSkinRules(tree("<h3>A</h3><h4>B</h4>"), rules)).toEqual([]);
	});

	it("constrains nothing when the skin states no rule", () => {
		expect(
			checkSkinRules(tree("<h2>A</h2><h5>B</h5>"), resolveSkinRules(undefined)),
		).toEqual([]);
		expect(checkSkinRules(tree("<h2>A</h2>"), undefined)).toEqual([]);
	});

	it("throws naming every violation", () => {
		const rules = resolveSkinRules({
			slots: { "page-title": { maxPerPage: 1 } },
			headingOrder: "no-skip",
		});
		expect(() =>
			assertSkinRules(
				tree(
					'<h1 data-slot="page-title">One</h1><h1 data-slot="page-title">Two</h1><h3>Deep</h3>',
				),
				rules,
			),
		).toThrow(/skin rules violated \(2\)/);
	});
});
