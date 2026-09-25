import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

// Two default-deny guards over the content tier's source.
//
// 1. No `dangerouslySetInnerHTML` and no `innerHTML`: every rendering path in
//    this tier builds React elements from a syntax tree. A future "just render
//    the highlighter's HTML" shortcut fails here, not in a penetration test.
// 2. No literal colour: the tier colours through the host's token utilities
//    (`text-chart-2`, `bg-muted`, `border-border`) so a skin re-themes content,
//    dark mode included. A hex, rgb/hsl/oklch/oklab/lab/lch function or a
//    Tailwind palette colour (`text-blue-500`, `bg-white`) is refused.

function contentDir(): string {
	for (const candidate of ["src/content", "packages/codefly-ui/src/content"]) {
		if (existsSync(join(candidate, "index.ts"))) return candidate;
	}
	throw new Error("could not locate the @codefly-dev/ui content tier");
}

// Comments may name what is refused (this tier's own explain why it never uses
// innerHTML), so both guards read code only.
function code(file: string): string {
	return readFileSync(file, "utf8")
		.replace(/\/\*[\s\S]*?\*\//g, "")
		.replace(/(^|[^:])\/\/.*$/gm, "$1");
}

function sources(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		if (entry.name === "__tests__") return [];
		const full = join(dir, entry.name);
		if (entry.isDirectory()) return sources(full);
		return /\.tsx?$/.test(entry.name) ? [full] : [];
	});
}

const PALETTE =
	"slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|white|black";
const LITERAL_COLOUR = new RegExp(
	[
		String.raw`#[0-9a-fA-F]{3,8}\b`,
		String.raw`\b(?:rgba?|hsla?|oklch|oklab|lab|lch|color)\(`,
		String.raw`\b(?:bg|text|border|fill|stroke|ring|outline|decoration|accent|caret|from|via|to|shadow)-(?:${PALETTE})(?:-\d{2,3})?\b`,
		String.raw`\[(?:color|background|background-color):`,
	].join("|"),
);

const dir = contentDir();
const files = sources(dir);

it("scans the content tier", () => {
	expect(files.some((file) => file.endsWith("markdown.tsx"))).toBe(true);
});

describe("content tier renders no HTML strings", () => {
	for (const file of files) {
		it(relative(dir, file), () => {
			expect(code(file)).not.toMatch(
				/dangerouslySetInnerHTML|\.innerHTML\b|\.outerHTML\b|insertAdjacentHTML/,
			);
		});
	}
});

describe("content tier colours only through tokens", () => {
	it("the detector catches what it claims to", () => {
		for (const bad of [
			"#fff",
			"rgb(0 0 0)",
			"oklch(0.5 0 0)",
			"text-blue-500",
			"bg-white",
			"[color:red]",
		]) {
			expect(LITERAL_COLOUR.test(bad), bad).toBe(true);
		}
		for (const good of [
			"text-chart-2",
			"bg-muted/40",
			"border-border",
			"text-muted-foreground",
			"accent-primary",
		]) {
			expect(LITERAL_COLOUR.test(good), good).toBe(false);
		}
	});

	for (const file of files) {
		it(relative(dir, file), () => {
			expect(code(file).match(LITERAL_COLOUR)?.[0]).toBeUndefined();
		});
	}
});
