import { existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";

// A component rendered from a solution REMOTE is laid out by whatever the host
// already compiled, and nothing else.
//
// The host builds one stylesheet from its own sources — this kit and
// `@codefly-dev/saas-ui` — so a class exists in the page only because something
// the host compiled wrote it. An ordinary token utility (`bg-muted`, `p-3`,
// `type-card-title`) is written by the host's own components too, so it is
// there. An arbitrary value (`grid-cols-[minmax(0,1fr)_18rem]`) and a
// breakpoint variant (`md:absolute`) exist only because ONE file wrote them, and
// the failure when they are missing is silent: jsdom lays nothing out, so the
// kit's own tests pass, and a reviewer sees the component correctly in a
// preview that does compile them. Measured once already, on a composed module's
// kit: a margin grid that computed to one column and put every comment card
// 8,000px below the text it was about (the composing workspace's handbook
// proposal "A module kit carries its own structure", 4 October 2026).
//
// So a component on this list carries its structure itself — an inline `style`
// for display, position, grid and flex templates, sizes and insets — and takes
// only colour, type and spacing from the tokens.
//
// The list is what has been AUDITED, and it grows. It is not the whole kit: the
// primitives in `layout/` predate the rule and carry arbitrary variants
// (`data-[side=bottom]:`, `rounded-[var(--appearance-…)]`), and `content/`
// carries three. Widening this to the whole kit with a shrinking baseline is the
// composing workspace's handbook proposal's ask and waits for it to be
// accepted; pinning the
// components written against the rule keeps them from drifting back meanwhile.
const REMOTE_SAFE = ["board/board.tsx"];

// String literals only, so this file's own explanation of what it refuses — and
// a component's header comment — is prose rather than a breach. Three things this
// gets right that are easy to get wrong:
//
//   - **Template literals count.** A class list written in backticks is still a
//     class list; a guard that reads only `"` and `'` lets `md:flex` through the
//     moment someone interpolates a variable into the list.
//   - Every literal is scanned, not only the ones that look like a class list.
//     An arbitrary value holds `(`, `,` and `_`, which the raw-type guard's
//     "looks like a class" filter drops — so that filter would have missed
//     `grid-cols-[minmax(0,1fr)_18rem]`, the exact failure this exists for.
//   - `${…}` holes are blanked rather than read, so an interpolated CSS length
//     inside an inline style is not mistaken for a utility.
function stringLiterals(source: string): string[] {
	const out: string[] = [];
	for (const match of source.matchAll(/"([^"\n]*)"|'([^'\n]*)'/g)) {
		out.push(match[1] ?? match[2] ?? "");
	}
	for (const match of source.matchAll(/`((?:[^`\\]|\\.)*)`/g)) {
		out.push((match[1] ?? "").replace(/\$\{[^}]*\}/g, " "));
	}
	return out;
}

// Any class carrying a bracketed group, wherever it sits: an arbitrary value
// (`h-[var(--x)]`), an arbitrary variant (`data-[state=open]:bg-muted`), or a
// bare property (`[overflow-wrap:anywhere]`). Matching the whole token rather
// than a shape keeps a spelling nobody anticipated from passing.
const ARBITRARY = /(?:^|[\s"'`])([^\s"'`]*\[[^\]]+\][^\s"'`]*)/g;
const BREAKPOINT =
	/(?:^|[\s"'`])((?:sm|md|lg|xl|2xl|min|max|container):[^\s"'`]+)/g;

export function hostCompiledOnly(source: string): string[] {
	const found: string[] = [];
	for (const value of stringLiterals(source)) {
		for (const match of value.matchAll(ARBITRARY))
			found.push(match[1] as string);
		for (const match of value.matchAll(BREAKPOINT))
			found.push(match[1] as string);
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

const srcDir = kitSrcDir();

describe("a kit component meant to render from a remote carries its own structure", () => {
	for (const name of REMOTE_SAFE) {
		it(`${name} needs no class the host did not already compile`, () => {
			const file = join(srcDir, name);
			expect(
				existsSync(file),
				`${name} is on the list but not in the tree`,
			).toBe(true);
			const found = hostCompiledOnly(readFileSync(file, "utf8"));
			expect(
				found,
				`${name} writes ${[...new Set(found)].join(", ")} — a class that exists only because this file wrote it. Put the structure in an inline style; keep colour, type and spacing on the tokens.`,
			).toEqual([]);
		});
	}

	it("places the board's columns with an inline style, not a class", () => {
		const source = readFileSync(join(srcDir, "board", "board.tsx"), "utf8");
		// The one structural decision a class could not carry here: how many
		// columns fit before the board wraps.
		expect(source).toContain("gridTemplateColumns");
		expect(source).not.toMatch(/grid-cols-/);
	});
});

// The guard is only worth having if it fires. A green run must mean "nothing a
// remote would lose", never "the detector was quietly disarmed".
describe("host-compiled-only detector (self-test)", () => {
	it.each([
		[
			'<div className="grid-cols-[minmax(0,1fr)_18rem]" />',
			"grid-cols-[minmax(0,1fr)_18rem]",
		],
		[
			'<div className="h-[var(--anchored-margin)]" />',
			"h-[var(--anchored-margin)]",
		],
		['<div className="md:absolute" />', "md:absolute"],
		[
			'<div className="sm:group-hover/code:opacity-100" />',
			"sm:group-hover/code:opacity-100",
		],
		[
			'<div className="[overflow-wrap:anywhere]" />',
			"[overflow-wrap:anywhere]",
		],
		[
			'<div className="data-[side=bottom]:slide-in-from-top-2" />',
			"data-[side=bottom]:slide-in-from-top-2",
		],
		// A class list in backticks, with a hole in it: the form that slipped past
		// the first version of this guard.
		["const x = `md:flex ${gap}`;", "md:flex"],
	])("flags %s", (source, utility) => {
		expect(hostCompiledOnly(source)).toContain(utility);
	});

	it.each([
		'<div className="bg-muted p-3 rounded-lg border border-border" />',
		'<div className="type-card-title-sm text-muted-foreground" />',
		'<div className="control-sm w-full text-left" />',
		"style={{ gridTemplateColumns: `repeat(auto-fit, minmax(16rem, 1fr))` }}",
	])("allows %s", (source) => {
		expect(hostCompiledOnly(source)).toEqual([]);
	});
});
