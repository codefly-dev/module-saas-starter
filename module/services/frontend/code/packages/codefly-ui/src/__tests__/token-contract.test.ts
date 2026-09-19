import { existsSync, readFileSync } from "node:fs";
import {
	DEFAULT_CONTROL_SIZES,
	DEFAULT_FRONTEND_APPEARANCE,
	DEFAULT_TYPE_ROLES,
	DEFAULT_TYPE_SCALE,
	DEFAULT_TYPE_SLOTS,
	FRONTEND_APPEARANCE_TOKEN_NAMES,
	FRONTEND_CONTROL_SIZE_NAMES,
	FRONTEND_TYPE_ROLE_NAMES,
	FRONTEND_TYPE_SCALE_STEPS,
	FRONTEND_TYPE_SLOT_NAMES,
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

it("delimits every TOKENS.md table with start/end markers", () => {
	for (const marker of [
		"token-table",
		"structural-table",
		"type-scale-table",
		"type-roles-table",
		"type-slots-table",
		"control-sizes-table",
	]) {
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

// The four layers document a vocabulary a skin author writes against, so the
// same drift guard the colour tokens have applies: a role renamed, a default
// changed, or a slot re-pointed in code while the document still describes the
// old one would send an author at a name that no longer exists.
const typeScaleRegion = tableRegion(doc, "type-scale-table");
const typeRolesRegion = tableRegion(doc, "type-roles-table");
const typeSlotsRegion = tableRegion(doc, "type-slots-table");
const controlSizesRegion = tableRegion(doc, "control-sizes-table");

/** A role's row prints an undeclared property as an em dash, not as a blank cell. */
function cell(value: string | undefined): string {
	return value === undefined ? "—" : `\`${value}\``;
}

describe("TOKENS.md documents the type scale", () => {
	for (const step of FRONTEND_TYPE_SCALE_STEPS) {
		it(`documents step ${step} and its default`, () => {
			const row = rowContaining(typeScaleRegion, step);
			expect(row, `TOKENS.md has no scale row for step ${step}`).toBeDefined();
			expect(row as string).toContain(`\`${DEFAULT_TYPE_SCALE[step]}\``);
		});
	}

	it("documents no step outside the contract", () => {
		const documented = [...typeScaleRegion.matchAll(/^\| `(\d+)` \|/gm)].map(
			(match) => match[1],
		);
		expect(documented.sort()).toEqual([...FRONTEND_TYPE_SCALE_STEPS].sort());
	});
});

describe("TOKENS.md documents every type role", () => {
	for (const name of FRONTEND_TYPE_ROLE_NAMES) {
		it(`documents ${name} with every property it decides`, () => {
			const row = rowContaining(typeRolesRegion, name);
			expect(row, `TOKENS.md has no role row for ${name}`).toBeDefined();
			const role = DEFAULT_TYPE_ROLES[name];
			const line = row as string;
			for (const value of [
				cell(role.size),
				cell(role.weight),
				cell(role.lineHeight),
				cell(role.tracking),
				cell(role.family),
			]) {
				expect(line, `${name} row must carry ${value}`).toContain(value);
			}
		});
	}

	it("documents no role outside the contract", () => {
		const documented = [
			...typeRolesRegion.matchAll(/^\| `([a-z0-9-]+)` \|/gm),
		].map((match) => match[1]);
		expect(documented.sort()).toEqual([...FRONTEND_TYPE_ROLE_NAMES].sort());
	});
});

describe("TOKENS.md documents which role each slot uses", () => {
	for (const slot of FRONTEND_TYPE_SLOT_NAMES) {
		it(`documents ${slot} under the role it uses`, () => {
			const role = DEFAULT_TYPE_SLOTS[slot];
			const row = rowContaining(typeSlotsRegion, role);
			expect(row, `TOKENS.md has no slot row for role ${role}`).toBeDefined();
			expect(
				row as string,
				`${slot} is missing from the ${role} row`,
			).toContain(`\`${slot}\``);
		});
	}

	it("lists no slot the contract does not declare", () => {
		const documented = new Set(
			[...typeSlotsRegion.matchAll(/`([a-z0-9-]+)`/g)].map((match) => match[1]),
		);
		const declared = new Set<string>([
			...FRONTEND_TYPE_SLOT_NAMES,
			...FRONTEND_TYPE_ROLE_NAMES,
		]);
		for (const name of documented)
			expect(
				declared.has(name),
				`TOKENS.md names '${name}', which is neither a slot nor a role`,
			).toBe(true);
	});
});

describe("TOKENS.md documents the control rungs", () => {
	for (const size of FRONTEND_CONTROL_SIZE_NAMES) {
		it(`documents rung ${size} with its geometry and text role`, () => {
			const row = rowContaining(controlSizesRegion, size);
			expect(row, `TOKENS.md has no rung row for ${size}`).toBeDefined();
			const rung = DEFAULT_CONTROL_SIZES[size];
			const line = row as string;
			for (const value of [rung.height, rung.paddingX, rung.icon, rung.text])
				expect(line, `${size} row must carry \`${value}\``).toContain(
					`\`${value}\``,
				);
		});
	}
});

// Self-test: the layer tables are checked by the same row-scoped detector as the
// colour tables, so prove it still refuses a wrong value on the right row.
describe("layer table detector (self-test)", () => {
	const sample =
		"<!-- type-roles-table:start -->\n| `body` | `3` | — | `1.25rem` | — | — |\n| `emphasis` | — | `500` | — | — | — |\n<!-- type-roles-table:end -->";
	const region = tableRegion(sample, "type-roles-table");

	it("finds a role row and its declared properties", () => {
		expect(rowContaining(region, "body") as string).toContain("`1.25rem`");
	});

	it("is not fooled by another row carrying the value", () => {
		expect(rowContaining(region, "emphasis") as string).not.toContain(
			"`1.25rem`",
		);
	});
});
