import { readFileSync } from "node:fs";
import {
	FRONTEND_APPEARANCE_TOKEN_NAMES,
	resolveFrontendAppearance,
} from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";
import {
	appearanceStyleProperties,
	appearanceVariableName,
	readableForeground,
} from "../appearance";

describe("application appearance projection", () => {
	it("projects both resolved palettes into SSR-safe variables", () => {
		const appearance = resolveFrontendAppearance({
			radius: "0.8rem",
			light: { primary: "#112233" },
			dark: { primary: "#ddeeff" },
		});
		const style = appearanceStyleProperties(appearance);

		expect(style["--appearance-radius"]).toBe("0.8rem");
		expect(style["--appearance-light-primary"]).toBe("#112233");
		expect(style["--appearance-dark-primary"]).toBe("#ddeeff");
		// Structural tokens fall back to the neutral defaults when omitted.
		expect(style["--appearance-spacing"]).toBe("0.25rem");
		expect(style["--appearance-font-size-base"]).toBe("1rem");
		expect(style["--appearance-sidebar-width"]).toBe("16rem");
		expect(style["--appearance-sidebar-width-icon"]).toBe("3rem");
		expect(style["--appearance-border-width"]).toBe("1px");
		expect(style["--appearance-shadow-strength"]).toBe("1");
		expect(style["--appearance-light-card-foreground"]).toBe(
			appearance.light.cardForeground,
		);
		expect(appearanceVariableName("dark", "sidebarPrimaryForeground")).toBe(
			"--appearance-dark-sidebar-primary-foreground",
		);
	});

	it("projects overridden structural tokens", () => {
		const style = appearanceStyleProperties(
			resolveFrontendAppearance({
				spacing: "0.2rem",
				fontSizeBase: "0.9375rem",
				sidebarWidth: "18rem",
				sidebarWidthIcon: "3.5rem",
				borderWidth: "2px",
				shadowStrength: "1.5",
			}),
		);
		expect(style["--appearance-spacing"]).toBe("0.2rem");
		expect(style["--appearance-font-size-base"]).toBe("0.9375rem");
		expect(style["--appearance-sidebar-width"]).toBe("18rem");
		expect(style["--appearance-sidebar-width-icon"]).toBe("3.5rem");
		expect(style["--appearance-border-width"]).toBe("2px");
		expect(style["--appearance-shadow-strength"]).toBe("1.5");
	});

	it("selects a readable foreground for tenant colors", () => {
		expect(readableForeground("#ffffff")).toBe("#000000");
		expect(readableForeground("#000000")).toBe("#ffffff");
		expect(readableForeground("#111827")).toBe("#ffffff");
	});
});

// The shadcn variable a token is consumed by: camelCase → kebab, with a dash
// before the digit in the chart tokens (`chart1` → `--chart-1`).
function shadcnVariable(token: string): string {
	return `--${token
		.replace(/([a-z])([0-9])/g, "$1-$2")
		.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
}

// The flat CSS rule block for a selector (no nested braces in these blocks).
function cssBlock(source: string, selector: string): string {
	const open = source.indexOf(`${selector} {`);
	if (open < 0) throw new Error(`globals.css has no ${selector} block`);
	const close = source.indexOf("\n}", open);
	if (close < 0)
		throw new Error(`globals.css ${selector} block is unterminated`);
	return source.slice(open, close);
}

// The contract owns the token names; globals.css is where they become the
// shadcn variables every layer above consumes. This ties the two: if a token's
// consumed variable is renamed in globals.css (or a new token gains no binding),
// the semantic utility that reads it silently breaks with no other test firing.
describe("globals.css binds every contract token to its consumed variable", () => {
	const globals = readFileSync("src/app/globals.css", "utf8");
	const light = cssBlock(globals, ":root");
	const dark = cssBlock(globals, ".dark");

	for (const token of FRONTEND_APPEARANCE_TOKEN_NAMES) {
		it(`defines ${shadcnVariable(token)} in :root and .dark`, () => {
			expect(light, `:root must define ${shadcnVariable(token)}`).toContain(
				`${shadcnVariable(token)}:`,
			);
			expect(dark, `.dark must define ${shadcnVariable(token)}`).toContain(
				`${shadcnVariable(token)}:`,
			);
		});
	}
});
