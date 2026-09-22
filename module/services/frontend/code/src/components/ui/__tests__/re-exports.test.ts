import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

// The v1 shadcn primitives were promoted out of this host directory into the kit
// as their single sealed home (issue #451, sealed layers #450). Each host file
// here is now a thin re-export of `@codefly-dev/ui/layout`, keeping the
// `@/components/ui/…` import path stable while the component ships exactly once
// from the kit.
//
// This guard is what keeps that invariant true over time: it fails if a promoted
// primitive is re-inlined back into the host (a component body, a `cva` variant
// set, or a `cn(...)` className call reappearing here). Without it, a future edit
// could fork a primitive — the host copy and the kit copy drifting apart — with
// nothing to catch it, which is the exact regression the promotion prevents.

const uiDir = join(dirname(fileURLToPath(import.meta.url)), "..");

const PROMOTED_PRIMITIVES = [
	"card",
	"tabs",
	"command",
	"input-group",
	"sheet",
	"sidebar",
	"sonner",
	"alert-dialog",
	"avatar",
	"badge",
	"button",
	"checkbox",
	"dialog",
	"dropdown-menu",
	"input",
	"label",
	"select",
	"separator",
	"skeleton",
	"switch",
	"table",
	"textarea",
	"tooltip",
] as const;

// Telltale signs a primitive was re-inlined rather than re-exported: it defines
// its own class strings (`cva(`), joins classes locally (`cn(`), or imports the
// upstream primitive directly instead of the kit.
const RE_INLINE_MARKERS = ["cva(", "cn(", "@base-ui/react", "lucide-react"];

describe("promoted UI primitives stay kit re-exports", () => {
	for (const name of PROMOTED_PRIMITIVES) {
		it(`${name}.tsx re-exports from @codefly-dev/ui/layout and is not re-inlined`, () => {
			const source = readFileSync(join(uiDir, `${name}.tsx`), "utf8");
			expect(source).toContain('from "@codefly-dev/ui/layout"');
			expect(hasIntrinsicJsx(source)).toBe(false);
			if (name !== "sidebar" && name !== "sonner")
				expect(source).not.toMatch(/\bfunction\b|=>/);
			for (const marker of RE_INLINE_MARKERS) {
				expect(
					source.includes(marker),
					`${name}.tsx re-inlines a primitive (found "${marker}") instead of re-exporting the kit`,
				).toBe(false);
			}
		});
	}
});

it("accounts for every host control file", () => {
	expect(
		readdirSync(uiDir)
			.filter((name) => name.endsWith(".tsx"))
			.map((name) => name.slice(0, -4))
			.sort(),
	).toEqual([...PROMOTED_PRIMITIVES].sort());
});

function hasIntrinsicJsx(source: string): boolean {
	const file = ts.createSourceFile(
		"control.tsx",
		source,
		ts.ScriptTarget.Latest,
		true,
		ts.ScriptKind.TSX,
	);
	let found = false;
	function visit(node: ts.Node) {
		if (
			(ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) &&
			/^[a-z]/.test(node.tagName.getText(file))
		)
			found = true;
		ts.forEachChild(node, visit);
	}
	visit(file);
	return found;
}

it("detects a host implementation without mistaking type arguments for JSX", () => {
	expect(hasIntrinsicJsx("const view = <button>Save</button>;")).toBe(true);
	expect(
		hasIntrinsicJsx("type Props = ComponentProps<typeof KitToaster>;"),
	).toBe(false);
	expect(hasIntrinsicJsx("const view = <KitToaster />;")).toBe(false);
});
