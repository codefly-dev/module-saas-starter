import { existsSync, readFileSync } from "node:fs";
import { FRONTEND_APPEARANCE_TOKEN_NAMES } from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";

// The contract owns the token names; theme.css is where each becomes a variable
// and a utility. A token missing from either half fails nowhere at runtime: a
// component writing `text-success` just renders in the inherited colour, which
// is exactly how a status tone went unstyled before this file carried one.

// Two Vitest projects with different cwds, as in token-contract.test.ts.
function themePath(): string {
	for (const path of [
		"src/skin/theme.css",
		"packages/codefly-ui/src/skin/theme.css",
	]) {
		if (existsSync(path)) return path;
	}
	throw new Error("could not locate the @codefly-dev/ui theme.css");
}

// `chart1` → `--chart-1`, `cardForeground` → `--card-foreground`.
function cssVariable(token: string): string {
	return `--${token
		.replace(/([a-z])([0-9])/g, "$1-$2")
		.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
}

// The flat rule block for a selector (these blocks nest nothing). The last
// one: the first `@theme inline` holds the shadcn keyframes, not the colours.
function cssBlock(source: string, selector: string): string {
	const open = source.lastIndexOf(`\n${selector} {`);
	if (open < 0) throw new Error(`theme.css has no ${selector} block`);
	const close = source.indexOf("\n}", open + 1);
	if (close < 0) throw new Error(`theme.css ${selector} block is unterminated`);
	return source.slice(open, close);
}

const theme = readFileSync(themePath(), "utf8");
const utilities = cssBlock(theme, "@theme inline");
const light = cssBlock(theme, ":root");
const dark = cssBlock(theme, ".dark");

describe("theme.css carries every contract token from skin to utility", () => {
	for (const token of FRONTEND_APPEARANCE_TOKEN_NAMES) {
		const variable = cssVariable(token);
		it(`binds ${variable} in both modes and maps it to a colour utility`, () => {
			expect(light, `:root must define ${variable}`).toContain(`${variable}:`);
			expect(dark, `.dark must define ${variable}`).toContain(`${variable}:`);
			expect(
				utilities,
				`@theme must map --color${variable.slice(1)} so bg-/text- utilities exist`,
			).toContain(`--color${variable.slice(1)}: var(${variable})`);
		});
	}
});
