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

// Each table is delimited so a row-scoped check reads only its own rows, not
// the other table or the prose (which name tokens and variables too). Scoping
// matters: `fontSans` and `fontHeading` share a default value, so a whole-doc
// substring check would let a wrong value in one row pass on the other's copy.
function tableRegion(source: string, marker: string): string {
	const start = source.indexOf(`<!-- ${marker}:start -->`);
	const end = source.indexOf(`<!-- ${marker}:end -->`);
	if (start < 0 || end <= start)
		throw new Error(`TOKENS.md is missing the ${marker} start/end markers`);
	return source.slice(start, end);
}

const colorRegion = tableRegion(doc, "token-table");
const structuralRegion = tableRegion(doc, "structural-table");

// The one table row that carries a given backticked key (a CSS variable or a
// token name). Each key is unique within its table, so the first match is it.
function rowContaining(region: string, key: string): string | undefined {
	return region.split("\n").find((line) => line.includes(`\`${key}\``));
}

it("delimits both TOKENS.md tables with start/end markers", () => {
	for (const marker of ["token-table", "structural-table"]) {
		expect(doc, `missing <!-- ${marker}:start -->`).toContain(
			`<!-- ${marker}:start -->`,
		);
		expect(doc, `missing <!-- ${marker}:end -->`).toContain(
			`<!-- ${marker}:end -->`,
		);
	}
});

describe("TOKENS.md documents exactly the contract token vocabulary", () => {
	for (const token of FRONTEND_APPEARANCE_TOKEN_NAMES) {
		it(`documents ${token} with its variable and light/dark defaults`, () => {
			const row = rowContaining(colorRegion, cssVariable(token));
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
		const documented = [...colorRegion.matchAll(/`(--[a-z0-9-]+)`/g)].map(
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
		it(`documents ${token} and its default on its own row`, () => {
			// Row-scoped so a wrong value can't pass on another token's identical
			// copy — `fontSans` and `fontHeading` share the same default string.
			const row = rowContaining(structuralRegion, token);
			expect(row, `TOKENS.md has no structural row for ${token}`).toBeDefined();
			expect(row as string, `${token} row must carry its default`).toContain(
				`\`${DEFAULT_FRONTEND_APPEARANCE[token]}\``,
			);
		});
	}
});

// Self-test: the guard is only useful if its row detector actually fires. Prove
// it catches a wrong value and, crucially, is NOT fooled by a correct copy of
// that value on another row — the exact masking a whole-doc check allowed.
describe("token-contract guard detector (self-test)", () => {
	const colorSample =
		"<!-- token-table:start -->\n| `border` | `--border` | Default hairline border | `oklch(0.922 0 0)` | `oklch(1 0 0 / 10%)` |\n<!-- token-table:end -->";

	it("finds a correctly documented color row", () => {
		const row = rowContaining(
			tableRegion(colorSample, "token-table"),
			"--border",
		);
		expect(row).toBeDefined();
		expect(row as string).toContain("`oklch(0.922 0 0)`");
	});

	it("rejects a wrong color default", () => {
		const row = rowContaining(
			tableRegion(colorSample, "token-table"),
			"--border",
		);
		expect(row as string).not.toContain("`oklch(0.5 0 0)`");
	});

	// fontHeading carries a WRONG value while fontSans's row holds the correct
	// shared string. A whole-doc check passes here (the string is present); the
	// row-scoped check must fail — that is the regression this guards.
	it("is not fooled by a shared value on a sibling row", () => {
		const structuralSample =
			"<!-- structural-table:start -->\n| `fontSans` | Body font | `Correct, sans-serif` |\n| `fontHeading` | Heading font | `WRONG, serif` |\n<!-- structural-table:end -->";
		const region = tableRegion(structuralSample, "structural-table");
		expect(structuralSample).toContain("`Correct, sans-serif`");
		const headingRow = rowContaining(region, "fontHeading") as string;
		expect(headingRow).not.toContain("`Correct, sans-serif`");
	});
});
