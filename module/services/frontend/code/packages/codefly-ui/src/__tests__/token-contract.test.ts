import { existsSync, readFileSync } from "node:fs";
import {
	DEFAULT_FRONTEND_APPEARANCE,
	FRONTEND_APPEARANCE_TOKEN_NAMES,
} from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";

// Ties TOKENS.md — the prose token contract — to the code that owns the
// vocabulary. FRONTEND_APPEARANCE_TOKEN_NAMES and DEFAULT_FRONTEND_APPEARANCE
// are the single source of truth; this guard fails if the document lists a
// different set of tokens, a wrong default value, or a stale CSS variable name,
// so the contract can never silently drift from the code it documents.

// The kit runs under two Vitest projects with different cwds (its own node
// config at the package root, and the host's happy-dom `pure` project at the
// frontend code root). Locate TOKENS.md by cwd-relative candidates, matching
// no-reinlined-primitives.test.ts.
function tokensDocPath(): string {
	for (const path of ["TOKENS.md", "packages/codefly-ui/TOKENS.md"]) {
		if (existsSync(path)) return path;
	}
	throw new Error("could not locate the @codefly-dev/ui TOKENS.md");
}

// The public shadcn variable name for a token, matching src/app/globals.css:
// camelCase → kebab, with a dash before the digit in the chart tokens
// (`chart1` → `--chart-1`).
function cssVariable(token: string): string {
	return `--${token
		.replace(/([a-z])([0-9])/g, "$1-$2")
		.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
}

const doc = readFileSync(tokensDocPath(), "utf8");

// The color table is delimited so the extra-token guard scans only it, not the
// structural table or the prose (which name variables too).
function colorTableRegion(source: string): string {
	const start = source.indexOf("<!-- token-table:start -->");
	const end = source.indexOf("<!-- token-table:end -->");
	expect(
		start,
		"TOKENS.md is missing the token-table:start marker",
	).toBeGreaterThanOrEqual(0);
	expect(
		end,
		"TOKENS.md is missing the token-table:end marker",
	).toBeGreaterThan(start);
	return source.slice(start, end);
}

const region = colorTableRegion(doc);

// The one table row that documents a token, matched by its unique CSS variable.
function rowFor(cssVar: string): string | undefined {
	return region.split("\n").find((line) => line.includes(`\`${cssVar}\``));
}

describe("TOKENS.md documents exactly the contract token vocabulary", () => {
	for (const token of FRONTEND_APPEARANCE_TOKEN_NAMES) {
		it(`documents ${token} with its variable and light/dark defaults`, () => {
			const row = rowFor(cssVariable(token));
			expect(
				row,
				`TOKENS.md has no color-table row for ${cssVariable(token)}`,
			).toBeDefined();
			// row is defined per the assertion above.
			const line = row as string;
			expect(line, `${token} row must name the contract token`).toContain(
				`\`${token}\``,
			);
			expect(line, `${token} row must carry its light default`).toContain(
				`\`${DEFAULT_FRONTEND_APPEARANCE.light[token]}\``,
			);
			expect(line, `${token} row must carry its dark default`).toContain(
				`\`${DEFAULT_FRONTEND_APPEARANCE.dark[token]}\``,
			);
		});
	}

	// The reverse direction: the table must document no token that is not in the
	// contract, so a removed or renamed token cannot linger in the doc.
	it("lists no color token outside FRONTEND_APPEARANCE_TOKEN_NAMES", () => {
		const documented = [...region.matchAll(/`(--[a-z0-9-]+)`/g)].map(
			(match) => match[1],
		);
		const expected = FRONTEND_APPEARANCE_TOKEN_NAMES.map(cssVariable);
		expect([...documented].sort()).toEqual([...expected].sort());
	});
});

describe("TOKENS.md documents the structural and typographic tokens", () => {
	const structural = [
		"defaultTheme",
		"radius",
		"fontSans",
		"fontHeading",
		"spacing",
		"fontSizeBase",
		"sidebarWidth",
		"sidebarWidthIcon",
		"borderWidth",
		"shadowStrength",
	] as const;

	for (const token of structural) {
		it(`documents ${token} and its default`, () => {
			expect(doc, `TOKENS.md must name ${token}`).toContain(`\`${token}\``);
			expect(doc, `TOKENS.md must carry the ${token} default`).toContain(
				`\`${DEFAULT_FRONTEND_APPEARANCE[token]}\``,
			);
		});
	}
});

// Self-test: the guard is only useful if its row detector actually fires. Prove
// it reports a mismatch on a wrong value and a hit on a correct one, so a green
// run means "doc matches the contract", never "the detector was disarmed".
describe("token-contract guard detector (self-test)", () => {
	const sample =
		"<!-- token-table:start -->\n| `border` | `--border` | Default hairline border | `oklch(0.922 0 0)` | `oklch(1 0 0 / 10%)` |\n<!-- token-table:end -->";

	it("finds a correctly documented row", () => {
		const line = sample.split("\n").find((l) => l.includes("`--border`"));
		expect(line).toBeDefined();
		expect(line).toContain("`oklch(0.922 0 0)`");
	});

	it("rejects a wrong default value", () => {
		const line = sample.split("\n").find((l) => l.includes("`--border`"));
		expect(line).not.toContain("`oklch(0.5 0 0)`");
	});
});
