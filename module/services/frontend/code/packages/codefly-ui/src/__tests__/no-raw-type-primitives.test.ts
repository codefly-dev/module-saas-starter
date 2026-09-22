import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";

import {
	FRONTEND_CONTROL_SIZE_NAMES,
	FRONTEND_TYPE_SLOT_NAMES,
} from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";

// Default-deny guard for the type half of the token contract, the counterpart to
// the one colour already has.
//
// A primitive and a semantic utility are indistinguishable in a class list:
// `bg-primary` reskins, `text-sm font-medium h-8` is welded in. So the only way
// to keep the kit reskinnable is to refuse the primitive outright. A component
// names its SLOT (`type-card-title`) or its control RUNG (`control-sm`) and the
// skin decides what that renders as.
//
// Bracketed sizes are called out separately because they are worse than a raw
// utility: `text-[10px]` is off any scale, so it cannot be reskinned, cannot be
// audited, and would survive a guard that only matched the named steps.

// Font families are tokens, not decisions, so `font-sans` / `font-heading` /
// `font-mono` are not matched: the family alternation below lists weights only.
const RAW_TYPE_UTILITY =
	/(?:^|[\s"'`])(?:[a-z0-9-]+(?:\[[^\]]*\])?(?:\/[a-z0-9-]+)?:)*(text-(?:xs|sm|base|lg|xl|[2-9]xl)|text-\[[^\]]+\]|font-(?:thin|extralight|light|normal|medium|semibold|bold|extrabold|black)|leading-[a-z0-9-]+|leading-\[[^\]]+\]|tracking-[a-z]+|tracking-\[[^\]]+\])(?=$|[\s"'`])/g;

/**
 * Class strings only. Scanning whole source would flag prose in a comment that
 * merely mentions `text-sm` — including this file's own explanation of what it
 * refuses.
 */
function classStrings(source: string): string[] {
	return [...source.matchAll(/"([^"\n]*)"|'([^'\n]*)'/g)]
		.map((match) => match[1] ?? match[2] ?? "")
		.filter((value) =>
			/(?:^|\s)[a-z-]+[a-z0-9:[\]/.%=&*!-]*(?:\s|$)/.test(value),
		);
}

export function rawTypeUtilities(source: string): string[] {
	const found: string[] = [];
	for (const value of classStrings(source)) {
		for (const match of value.matchAll(RAW_TYPE_UTILITY)) found.push(match[1]);
	}
	return found;
}

function kitSrcDir(): string {
	for (const candidate of ["src", "packages/codefly-ui/src"]) {
		const absolute = resolve(process.cwd(), candidate);
		if (existsSync(join(absolute, "index.ts"))) return absolute;
	}
	throw new Error("could not locate the @codefly-dev/ui src directory");
}

function sourceFiles(dir: string): string[] {
	const out: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		if (entry.name === "__tests__") continue;
		const full = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...sourceFiles(full));
		else if (/\.tsx?$/.test(entry.name)) out.push(full);
	}
	return out;
}

const srcDir = kitSrcDir();
const files = sourceFiles(srcDir);

// `@codefly-dev/saas-ui` ships beside this kit at the same version and renders
// inside the same skin, so the guard covers it too. Its data-sources surface
// predates the slots and still hardcodes primitives; those files are pinned
// here at their EXACT counts so the debt can only shrink, and a line is deleted
// when its file reaches zero. Anything else in that package is default-deny.
const SAAS_UI_BASELINE: Record<string, number> = {
	"datasources/collection-access.tsx": 2,
	"datasources/connect-github-form.tsx": 16,
	"datasources/datasources-panel.tsx": 22,
};
const saasUiDir = resolve(srcDir, "../../saas-ui/src");
const saasUiFiles = existsSync(saasUiDir) ? sourceFiles(saasUiDir) : [];

describe("kit source names slots and rungs, never raw type primitives", () => {
	// A green run must mean "scanned real files", never "scanned nothing".
	it("scans a non-empty tree including a migrated component", () => {
		expect(files.length).toBeGreaterThan(0);
		expect(
			files.some((file) => file.endsWith(join("layout", "card-root.tsx"))),
		).toBe(true);
	});

	for (const file of files) {
		it(`${relative(srcDir, file)} carries no raw type utility`, () => {
			const found = rawTypeUtilities(readFileSync(file, "utf8"));
			expect(
				found,
				found.length > 0
					? `${relative(srcDir, file)} hardcodes ${[...new Set(found)].join(", ")} — name a type slot (type-<slot>) or a control rung instead`
					: undefined,
			).toEqual([]);
		});
	}
});

describe("saas-ui names slots and rungs, with a shrinking baseline", () => {
	it("scans the sibling package", () => {
		expect(saasUiFiles.length).toBeGreaterThan(0);
	});

	for (const file of saasUiFiles) {
		const name = relative(saasUiDir, file).split("\\").join("/");
		it(`${name} carries no raw type utility beyond its baseline`, () => {
			const found = rawTypeUtilities(readFileSync(file, "utf8"));
			const allowed = SAAS_UI_BASELINE[name] ?? 0;
			expect(
				found.length,
				found.length > allowed
					? `${name} hardcodes ${[...new Set(found)].join(", ")} — name a type slot (type-<slot>) or a control rung instead`
					: `${name} now has ${found.length} raw type utilities, fewer than its baseline of ${allowed}: lower the baseline (delete the line at zero)`,
			).toBe(allowed);
		});
	}

	it("keeps no baseline line for a file that is gone", () => {
		for (const name of Object.keys(SAAS_UI_BASELINE))
			expect(existsSync(join(saasUiDir, name)), name).toBe(true);
	});
});

// Detector self-tests: the guard is only useful if it fires. A green suite must
// mean "no primitive found", never "the detector was quietly disarmed".
describe("raw-type detector (self-test)", () => {
	it.each([
		['<div className="text-sm" />', "text-sm"],
		['<div className="font-medium" />', "font-medium"],
		['<div className="md:text-sm" />', "text-sm"],
		['<div className="group-data-[size=sm]/card:text-xs" />', "text-xs"],
		['<div className="text-[10px]" />', "text-[10px]"],
		['<div className="text-[0.8rem]" />', "text-[0.8rem]"],
		['<div className="leading-none" />', "leading-none"],
		['<div className="tracking-tight" />', "tracking-tight"],
	])("flags %s", (source, utility) => {
		expect(rawTypeUtilities(source)).toContain(utility);
	});

	it.each([
		'<div className="type-card-title" />',
		'<div className="control-sm" />',
		'<div className="font-heading" />',
		'<div className="text-muted-foreground" />',
		'<div className="text-left" />',
		'<div className="tabular-nums" />',
		'<div className="max-w-sm" />',
		'<div className="group-data-[size=sm]/card:type-card-title-sm" />',
	])("allows %s", (source) => {
		expect(rawTypeUtilities(source)).toEqual([]);
	});
});

// The vocabulary and the source must agree in BOTH directions. A slot nothing
// uses is a name a skin author can set with no effect; a `type-*` class with no
// slot behind it is a class that styles nothing, and neither fails at runtime.
describe("every slot and rung is real in both directions", () => {
	// Class strings only, for the same reason the raw-type scan is: prose in a
	// comment that merely names `control-height-<size>` is documentation, not a
	// class, and scanning whole source flags the explanation of the rule as a
	// breach of it.
	const used = new Set<string>();
	const rungs = new Set<string>();
	for (const file of files) {
		for (const value of classStrings(readFileSync(file, "utf8"))) {
			for (const match of value.matchAll(/(?<![\w-])type-([a-z0-9-]+)/g))
				used.add(match[1]);
			for (const match of value.matchAll(
				/(?<![\w-])control-(?:height-|icon-|glyph-)?([a-z]+)\b/g,
			))
				rungs.add(match[1]);
		}
	}

	it("uses every slot the contract declares", () => {
		const unused = FRONTEND_TYPE_SLOT_NAMES.filter((slot) => !used.has(slot));
		expect(
			unused,
			`the contract declares ${unused.join(", ")}, which no kit component names — either use the slot or drop it from the vocabulary`,
		).toEqual([]);
	});

	it("names no slot the contract does not declare", () => {
		const declared = new Set<string>(FRONTEND_TYPE_SLOT_NAMES);
		const undeclared = [...used].filter((slot) => !declared.has(slot));
		expect(
			undeclared,
			`kit source names type-${undeclared.join(", type-")}, which the contract does not declare — that class styles nothing`,
		).toEqual([]);
	});

	it("names no control rung the contract does not declare", () => {
		const declared = new Set<string>(FRONTEND_CONTROL_SIZE_NAMES);
		const undeclared = [...rungs].filter((rung) => !declared.has(rung));
		expect(
			undeclared,
			`kit source names control-${undeclared.join(", control-")}, which is not a declared rung`,
		).toEqual([]);
	});
});

// The button is where type was coupled to a size variant rather than to a slot.
// It is now the worked example of the other answer — the rung carries the type —
// so pin it: a size variant that grows its own type utility has regressed.
describe("the button sizes off control rungs", () => {
	const button = readFileSync(join(srcDir, "layout", "button.tsx"), "utf8");

	it.each([...FRONTEND_CONTROL_SIZE_NAMES])(
		"uses the %s rung rather than a literal height",
		(size) => {
			expect(button).toContain(`control-${size}`);
		},
	);

	it("keeps no literal height or inline padding in its size map", () => {
		expect(button).not.toMatch(/"(?:[^"]*\s)?h-\d/);
		expect(button).not.toMatch(/"(?:[^"]*\s)?px-\d/);
		expect(button).not.toMatch(/"(?:[^"]*\s)?size-\d/);
	});
});
