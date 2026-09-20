import { readFileSync } from "node:fs";
import { join } from "node:path";
import {
	DEFAULT_FRONTEND_APPEARANCE,
	resolveFrontendAppearance,
} from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";
import { FONT_CATALOG } from "../../app/fonts";
import { appearanceStyleProperties } from "../appearance";

// The tokens a design sheet specifies and the compiled default cannot guess:
// a button's own corner, and the primary action's hover / pressed / disabled
// fills. Each is projected only when the skin decided it, and the stylesheet's
// `var(--…, fallback)` derives the rest — so the default render is untouched
// and a skin that carries them gets exactly what it wrote.
describe("optional design tokens are projected only when decided", () => {
	it("leaves every optional variable off the default skin", () => {
		const properties = appearanceStyleProperties(DEFAULT_FRONTEND_APPEARANCE);
		for (const name of [
			"--appearance-button-radius",
			"--appearance-light-primary-hover",
			"--appearance-light-primary-active",
			"--appearance-light-disabled",
			"--appearance-light-disabled-foreground",
			"--appearance-light-disabled-opacity",
			"--appearance-dark-disabled-opacity",
		])
			expect(properties, name).not.toHaveProperty(name);
		expect(properties["--appearance-font-mono"]).toBe(
			DEFAULT_FRONTEND_APPEARANCE.fontMono,
		);
	});

	it("projects what a skin decided, plus the disabled presence signal", () => {
		const properties = appearanceStyleProperties(
			resolveFrontendAppearance({
				buttonRadius: "999px",
				fontMono: '"JetBrains Mono", monospace',
				light: {
					primaryHover: "#3B67A8",
					primaryActive: "#1C3256",
					disabled: "#ACB6C3",
					disabledForeground: "#2D3840",
				},
			}),
		);
		expect(properties["--appearance-button-radius"]).toBe("999px");
		expect(properties["--appearance-font-mono"]).toBe(
			'"JetBrains Mono", monospace',
		);
		expect(properties["--appearance-light-primary-hover"]).toBe("#3B67A8");
		expect(properties["--appearance-light-primary-active"]).toBe("#1C3256");
		expect(properties["--appearance-light-disabled"]).toBe("#ACB6C3");
		expect(properties["--appearance-light-disabled-foreground"]).toBe(
			"#2D3840",
		);
		// A decided disabled fill renders flat; the dark theme decided nothing.
		expect(properties["--appearance-light-disabled-opacity"]).toBe("1");
		expect(properties).not.toHaveProperty("--appearance-dark-disabled-opacity");
	});

	it("derives the interaction states in the stylesheet, in both themes", () => {
		const css = readFileSync(
			join(process.cwd(), "src/app/globals.css"),
			"utf8",
		);
		for (const mode of ["light", "dark"]) {
			expect(css).toContain(`--appearance-${mode}-primary-hover,`);
			expect(css).toContain(`--appearance-${mode}-disabled-opacity, 0.5`);
		}
		expect(css).toContain("--font-mono: var(--appearance-font-mono)");
	});
});

// A skin's family stack is only as real as the faces the document loads. The
// catalog is a list of side-effect imports; this holds the exported names to
// the imports so a face cannot be listed without being loaded, or loaded
// without being listed.
describe("the font catalog loads every family it names", () => {
	it("imports a face for each catalog entry", () => {
		const source = readFileSync(
			join(process.cwd(), "src/app/fonts.ts"),
			"utf8",
		);
		const imported = new Set(
			[...source.matchAll(/@fontsource\/([a-z0-9-]+)\//g)].map((m) => m[1]),
		);
		const slug = (family: string) => family.toLowerCase().replace(/ /g, "-");
		for (const family of FONT_CATALOG)
			expect(imported, family).toContain(slug(family));
		expect(imported.size).toBe(FONT_CATALOG.length);
	});
});
