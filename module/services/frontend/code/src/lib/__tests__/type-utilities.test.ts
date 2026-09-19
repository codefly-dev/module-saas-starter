import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
	DEFAULT_FRONTEND_APPEARANCE,
	FRONTEND_CONTROL_SIZE_NAMES,
	FRONTEND_TYPE_SLOT_NAMES,
} from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";

import { appearanceStyleProperties } from "../appearance";

// The stylesheet that turns the skin's slots into class names is generated. A
// slot added to the vocabulary and forgotten there would be a name that silently
// styles nothing, so the generator's own --check runs here.
describe("the generated type utilities stay in step with the contract", () => {
	it("is not stale", () => {
		expect(() =>
			execFileSync(
				process.execPath,
				["scripts/generate-type-utilities.mjs", "--check"],
				{ cwd: process.cwd(), stdio: "pipe" },
			),
		).not.toThrow();
	});

	it("declares one utility per slot and four per control rung", () => {
		const css = readFileSync(
			join(process.cwd(), "src/app/type-slots.generated.css"),
			"utf8",
		);
		for (const slot of FRONTEND_TYPE_SLOT_NAMES)
			expect(css, `no utility for slot '${slot}'`).toContain(
				`@utility type-${slot} {`,
			);
		for (const size of FRONTEND_CONTROL_SIZE_NAMES)
			for (const prefix of [
				"control",
				"control-height",
				"control-icon",
				"control-glyph",
			])
				expect(css, `no ${prefix} utility for rung '${size}'`).toContain(
					`@utility ${prefix}-${size} {`,
				);
	});

	it("is imported by the stylesheet the host compiles", () => {
		const globals = readFileSync(
			join(process.cwd(), "src/app/globals.css"),
			"utf8",
		);
		expect(globals).toContain('@import "./type-slots.generated.css";');
	});
});

describe("the resolved skin reaches those utilities as custom properties", () => {
	const properties = appearanceStyleProperties(DEFAULT_FRONTEND_APPEARANCE);

	it("projects every property a slot's role decides", () => {
		expect(properties["--type-card-title-size"]).toBe("1rem");
		expect(properties["--type-card-title-weight"]).toBe("500");
		expect(properties["--type-card-title-leading"]).toBe("1.375");
		expect(properties["--type-card-title-family"]).toBe("var(--font-heading)");
		expect(properties["--type-page-title-tracking"]).toBe("-0.025em");
	});

	// The load-bearing half: a property the role does NOT decide must stay unset,
	// so the utility's `var()` is invalid at computed-value time and the element
	// inherits — which is what the tree does today where nothing sets a size.
	it("leaves a property the role does not decide unset", () => {
		expect(properties["--type-table-head-weight"]).toBe("500");
		expect(properties).not.toHaveProperty("--type-table-head-size");
		expect(properties).not.toHaveProperty("--type-table-head-leading");
		expect(properties).not.toHaveProperty("--type-card-title-tracking");
	});

	// Geometry rides the density unit rather than a fixed length, so a skin that
	// changes `spacing` moves control geometry with it.
	it("projects control geometry as spacing multiples", () => {
		expect(properties["--control-default-height"]).toBe(
			"calc(var(--spacing) * 8)",
		);
		expect(properties["--control-default-padding-x"]).toBe(
			"calc(var(--spacing) * 2.5)",
		);
		expect(properties["--control-sm-icon"]).toBe("calc(var(--spacing) * 3.5)");
	});

	it("gives each rung the type role it carries", () => {
		expect(properties["--control-default-text-size"]).toBe("0.875rem");
		expect(properties["--control-sm-text-size"]).toBe("0.75rem");
		expect(properties["--control-default-text-weight"]).toBe("500");
	});

	it("follows a skin that re-points a slot or moves a step", () => {
		const skinned = appearanceStyleProperties({
			...DEFAULT_FRONTEND_APPEARANCE,
			typeSlots: {
				...DEFAULT_FRONTEND_APPEARANCE.typeSlots,
				"card-title": "page-title",
			},
		});
		expect(skinned["--type-card-title-size"]).toBe("1.5rem");
	});
});
