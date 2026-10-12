import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	DEFAULT_FRONTEND_APPEARANCE,
	FRONTEND_APPEARANCE_TOKEN_NAMES,
} from "@codefly-dev/saas-plugin-contract";
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import * as composites from "../../../packages/codefly-ui/stories/composites.stories";
import * as content from "../../../packages/codefly-ui/stories/content.stories";
import * as controls from "../../../packages/codefly-ui/stories/controls.stories";
import * as foundations from "../../../packages/codefly-ui/stories/foundations.stories";
import * as metrics from "../../../packages/codefly-ui/stories/metrics.stories";
import * as primitives from "../../../packages/codefly-ui/stories/primitives.stories";
import * as semanticTables from "../../../packages/codefly-ui/stories/semantic-table.stories";
import * as tables from "../../../packages/codefly-ui/stories/table.stories";
import { appearanceVariableName } from "../appearance";

const KIT = join(process.cwd(), "packages/codefly-ui");
const PREVIEW = join(KIT, "src/skin/preview.generated.css");
const preview = readFileSync(PREVIEW, "utf8");

function checkPreview(env: Record<string, string> = {}) {
	execFileSync(
		process.execPath,
		["scripts/generate-preview-stylesheet.mjs", "--check"],
		{ cwd: KIT, stdio: "pipe", env: { ...process.env, ...env } },
	);
}

afterEach(cleanup);

describe("the kit's own stylesheets are what the host compiles", () => {
	it("exports its token layer and its preview, and the host imports the layer", () => {
		const kit = JSON.parse(readFileSync(join(KIT, "package.json"), "utf8")) as {
			exports: Record<string, unknown>;
		};
		expect(kit.exports["./theme.css"]).toBe("./src/skin/theme.css");
		expect(kit.exports["./preview.css"]).toBe(
			"./src/skin/preview.generated.css",
		);
		const globals = readFileSync(
			join(process.cwd(), "src/app/globals.css"),
			"utf8",
		);
		expect(globals).toContain('@import "@codefly-dev/ui/theme.css";');
	});
});

// Its bytes depend on inputs outside the kit's folder — the contract's default
// skin, the Tailwind and Lightning CSS versions in the lockfile — which the
// kit-version gate cannot see. Committed and checked here, any of them moving
// shows up as a changed file in the kit, which the gate does see.
describe("preview.css is regenerated whenever an input moves", () => {
	it("is not stale", () => {
		expect(() => checkPreview()).not.toThrow();
	});

	it("refuses a copy that differs from what the inputs produce", () => {
		const stale = join(mkdtempSync(join(tmpdir(), "preview-")), "stale.css");
		writeFileSync(stale, `${preview}/* one byte off */\n`);
		expect(() => checkPreview({ PREVIEW_STYLESHEET_PATH: stale })).toThrow();
	});
});

describe("preview.css paints the kit without a host", () => {
	it("is plain CSS, with nothing left for a Tailwind build to resolve", () => {
		for (const directive of [
			"@import",
			"@theme",
			"@utility",
			"@apply",
			"@source",
			"@custom-variant",
		])
			expect(preview, `${directive} survived the compile`).not.toContain(
				`${directive} `,
			);
	});

	// What the host would have projected onto <html>, so every variable a kit
	// class reads resolves. Names, not values: the compile normalises a value's
	// spelling (`oklch(1 0 0)` becomes `oklch(100% 0 0)`), and the values are
	// the projection's own output.
	it("sets the default skin's values at the root", () => {
		for (const mode of ["light", "dark"] as const)
			for (const token of FRONTEND_APPEARANCE_TOKEN_NAMES) {
				if (DEFAULT_FRONTEND_APPEARANCE[mode][token] === undefined) continue;
				expect(preview).toContain(`${appearanceVariableName(mode, token)}:`);
			}
		expect(preview).toContain("--type-card-title-size:");
		expect(preview).toContain("--control-default-height:");
	});

	// The claim a solution relies on: a page built from kit components is fully
	// painted. Every class every story renders must be defined, or that element
	// previews unstyled while it looks fine in the host — the gap this file
	// exists to close, reopened silently.
	it("defines every class the kit's stories render", () => {
		const missing = new Set<string>();
		for (const stories of [
			composites,
			content,
			controls,
			foundations,
			metrics,
			primitives,
			semanticTables,
			tables,
		])
			for (const story of Object.values(stories)) {
				if (typeof story !== "object" || !story || !("render" in story))
					continue;
				const Story = story.render as () => React.ReactElement;
				const { baseElement } = render(<Story />);
				for (const element of baseElement.querySelectorAll("[class]"))
					for (const name of element.getAttribute("class")?.split(/\s+/) ?? [])
						if (
							name &&
							!MARKERS.test(name) &&
							!storyAuthored.has(name) &&
							!KNOWN_UNSTYLED.has(name) &&
							!defines(name)
						)
							missing.add(name);
				cleanup();
			}
		expect([...missing].sort()).toEqual([]);
	});
});

// Names that style nothing themselves: Tailwind's group/peer markers, which
// other classes select through, and the icon library's own name tags.
const MARKERS = /^(?:(?:group|peer)(?:\/[\w-]+)?|lucide(?:-[\w-]+)?)$/;

// A story lays its example out with classes of its own (`w-40` around a
// select); those are the story's, not the kit's, and preview.css is the kit.
const storyAuthored = new Set(
	[
		"composites",
		"content",
		"controls",
		"foundations",
		"metrics",
		"primitives",
		"semantic-table",
		"table",
	].flatMap((name) =>
		[
			...readFileSync(
				join(KIT, `stories/${name}.stories.tsx`),
				"utf8",
			).matchAll(/className="([^"]*)"/g),
		].flatMap((match) => match[1].split(/\s+/)),
	),
);

// Unstyled in the host too, so not a preview gap: a type slot's utility wraps
// its rule in `:where(&)`, and behind the `file:` variant that becomes
// `:where(.x::file-selector-button)` — a pseudo-element inside `:where()`,
// which a browser drops and the preview compile removes.
const KNOWN_UNSTYLED = new Set(["file:type-input"]);

// Tailwind's selector escaping: every character outside [A-Za-z0-9_-].
function escape(name: string): string {
	return name.replace(/[^A-Za-z0-9_-]/g, (character) => `\\${character}`);
}

// The class as a whole selector, not a prefix: `.border` must not be satisfied
// by `.border-border`, nor `.p-4` by `.p-4\.5`.
function defines(name: string): boolean {
	return new RegExp(
		`\\.${escape(name).replace(/[\\^$.*+?()[\]{}|]/g, "\\$&")}(?![A-Za-z0-9_\\\\-])`,
	).test(preview);
}
