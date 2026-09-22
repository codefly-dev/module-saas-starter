import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { compile } from "tailwindcss";
import { describe, expect, it } from "vitest";

// The kit's `cn` keeps a core utility BESIDE a slot or rung rather than treating
// it as a conflict (tailwind-merge can only drop a whole class, and these are
// composite). That is only correct because every generated utility sits at zero
// specificity: `text-lg` beats `type-card-title`'s size, and only its size,
// whatever the source order. This compiles the generated file through the
// Tailwind pinned here and checks the mechanism survived — a Tailwind that
// stopped flattening `:where(&)` inside `@utility` would silently hand the
// decision back to source order.
const CODE_ROOT = resolve(__dirname, "../../..");
const TAILWIND = resolve(CODE_ROOT, "node_modules/tailwindcss");

async function compileUtilities(candidates: string[]): Promise<string> {
	const generated = readFileSync(
		resolve(CODE_ROOT, "packages/codefly-ui/src/skin/type-slots.generated.css"),
		"utf8",
	);
	const compiler = await compile(`@import "tailwindcss";\n${generated}`, {
		base: CODE_ROOT,
		loadStylesheet: async (id, base) => {
			const path =
				id === "tailwindcss"
					? resolve(TAILWIND, "index.css")
					: resolve(base, id);
			return { path, base: TAILWIND, content: readFileSync(path, "utf8") };
		},
	});
	return compiler.build(candidates);
}

describe("generated type utilities compile at zero specificity", () => {
	it("wraps every slot and rung rule in :where()", async () => {
		const css = await compileUtilities([
			"type-card-title",
			"type-table-head",
			"control-lg",
			"control-height-sm",
			"control-icon-sm",
			"control-glyph-sm",
			"md:type-input",
			"[&_svg:not([class*='size-'])]:control-glyph-sm",
		]);
		const utilities = css.slice(css.indexOf("@layer utilities"));
		const selectors = utilities
			.split("\n")
			.filter((line) => line.trimEnd().endsWith("{") && !line.includes("@"))
			.map((line) => line.trim().slice(0, -1).trim());
		const skin = selectors.filter((selector) =>
			/type-|control-/.test(selector),
		);
		expect(skin.length).toBeGreaterThanOrEqual(8);
		for (const selector of skin)
			expect(selector, selector).toMatch(/^:where\(.*\)$/);
	});

	it("declares only the properties the slot's role decides", async () => {
		const css = await compileUtilities(["type-table-head", "type-input"]);
		const rule = (name: string) => {
			const start = css.indexOf(`:where(.${name})`);
			return css.slice(start, css.indexOf("}", start));
		};
		// `table-head` is weight alone; declaring a size over an unset variable
		// would not inherit, it would reset the table's size on the cell.
		expect(rule("type-table-head")).toContain("font-weight");
		expect(rule("type-table-head")).not.toContain("font-size");
		expect(rule("type-input")).not.toContain("font-family");
		expect(rule("type-input")).not.toContain("letter-spacing");
	});
});
