import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Default-deny guard against re-inlining a layout primitive's class string.
//
// The layout tier (`Card`, `Section`) owns the byte-identical class strings that
// paint a card surface or a section heading. Any other tier that hardcodes one of
// those strings instead of importing the primitive forks the styling: a skin or
// padding change then propagates to the primitive but silently skips the copy.
// This guard fails when a guarded string appears anywhere outside the `layout/`
// tier that owns it, so the only way to paint a card surface is `<Card>`.

// Class strings that belong to exactly one layout primitive. Each is distinctive
// enough that its presence outside `layout/` is a re-inline, not a coincidental
// overlap of common utilities.
const GUARDED_PRIMITIVES: Array<{ owner: string; class: string }> = [
	{
		owner: "Card (layout/card.tsx)",
		class: "rounded-lg border bg-card p-4 text-card-foreground shadow-sm",
	},
	{
		owner: "Section (layout/card.tsx)",
		class: "text-lg font-semibold tracking-tight",
	},
];

// The kit runs under two Vitest projects with different cwds (its own node config
// at the package root, and the host's happy-dom `pure` project at the frontend
// code root). Locate this package's `src` by cwd-relative candidates, matching
// package-contract.test.ts.
function codeflyUiSrcDir(): string {
	for (const path of ["src", "packages/codefly-ui/src"]) {
		if (existsSync(join(path, "index.ts"))) return path;
	}
	throw new Error("could not locate the @codefly-dev/ui src directory");
}

// Every source file outside the `layout/` tier and the test trees. `layout/` is
// where the guarded strings legitimately live; tests carry them as fixtures.
function nonLayoutSourceFiles(dir: string): string[] {
	const out: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		if (entry.name === "__tests__" || entry.name === "layout") continue;
		const full = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...nonLayoutSourceFiles(full));
		else if (/\.tsx?$/.test(entry.name)) out.push(full);
	}
	return out;
}

function reInlinedPrimitive(source: string): { owner: string; class: string } | undefined {
	return GUARDED_PRIMITIVES.find((primitive) => source.includes(primitive.class));
}

const srcDir = codeflyUiSrcDir();

describe("no re-inlined layout primitives outside the layout tier", () => {
	for (const file of nonLayoutSourceFiles(srcDir)) {
		it(`${file} composes the primitive instead of copying its class string`, () => {
			const hit = reInlinedPrimitive(readFileSync(file, "utf8"));
			expect(
				hit,
				hit && `${file} re-inlines the ${hit.owner} class string; import the primitive instead`,
			).toBeUndefined();
		});
	}

	// Self-test: the guard is only useful if its detector actually fires. Feed it
	// the card surface string and prove it reports a hit, so a green run means
	// "no re-inline found", never "the detector was silently disarmed".
	it("detects a re-inlined card surface string (self-test)", () => {
		const violating = `<div className="rounded-lg border bg-card p-4 text-card-foreground shadow-sm" />`;
		expect(reInlinedPrimitive(violating)?.owner).toBe("Card (layout/card.tsx)");
	});

	it("does not flag source that only imports the primitive", () => {
		const clean = `import { Card } from "../layout/card.js";\nexport const x = <Card />;`;
		expect(reInlinedPrimitive(clean)).toBeUndefined();
	});
});
