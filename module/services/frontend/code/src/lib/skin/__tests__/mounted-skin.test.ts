import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	type FrontendBranding,
	resolveFrontendAppearance,
	resolveTypeSlot,
} from "@codefly/saas-plugin-contract";
import {
	assertSkinSurvives,
	type RawSkinDescriptor,
	type ResolvedSkinBase,
	type SkinSource,
} from "@codefly-dev/ui/skin";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { clearSkinCache, resolveSkin } from "..";
import { fileSkinSource } from "../sources";

// A skin descriptor is owned by the product that deploys it and mounted into
// the frontend at runtime (FRONTEND_SKIN_DIR, see module/deployment/README.md).
// This module ships none: a consumer's brand is the consumer's, and a name in
// this tree is a name the naming gate refuses. What this module DOES own is the
// path such a file takes — mounted-file source → resolver → contract validator
// — and that is what this fixture exercises: a generic descriptor that sets
// every layer a real one can, so a contract rename breaks here instead of in a
// deployment.
const fixture: RawSkinDescriptor = {
	appearance: {
		defaultTheme: "light",
		radius: "1rem",
		fontSans: "Trebuchet MS, Segoe UI, sans-serif",
		fontHeading: "Georgia, serif",
		spacing: "0.28rem",
		sidebarWidth: "17rem",
		sidebarWidthIcon: "3.25rem",
		borderWidth: "1px",
		shadowStrength: "1.4",
		// Layers 1 to 3: a moved scale step, a re-weighted role, a re-pointed
		// slot (same shape) and a taller control rung.
		typeScale: { "7": "1.625rem" },
		typeRoles: {
			"page-title": { weight: "600", tracking: "-0.04em" },
			"surface-title-snug": { family: "heading", lineHeight: "1.5rem" },
		},
		typeSlots: {
			"card-description": "menu-item",
			"empty-state-title": "control-label",
		},
		controlSizes: { default: { height: "9", paddingX: "3" } },
		light: {
			primary: "oklch(0.68 0.19 45)",
			accent: "oklch(0.88 0.18 125)",
		},
		dark: {
			primary: "oklch(0.78 0.17 45)",
		},
	},
	branding: {
		name: "Acme",
		mark: "A",
		title: "Acme Console",
		description: "The operations console for Acme.",
		logo: {
			lightSrc: "https://cdn.example.com/acme-logo.svg",
			darkSrc: "https://cdn.example.com/acme-logo-dark.svg",
			alt: "Acme",
		},
	},
	// Layer 4: carried through as data, never rendered.
	rules: {
		slots: { "page-title": { maxPerPage: 1 } },
		headingOrder: "no-skip",
	},
};

// A neutral fallback that shares NONE of the fixture's distinctive values, so
// any assertion below can only pass if the mounted file actually resolved.
const branding: FrontendBranding = {
	name: "Starter",
	mark: "S",
	title: "Starter application",
	description: "Default description",
	favicon: "/favicon.ico",
};
const fallback: ResolvedSkinBase = {
	appearance: resolveFrontendAppearance(undefined),
	branding,
};

let dir: string;
beforeAll(() => {
	dir = mkdtempSync(join(tmpdir(), "skin-"));
	writeFileSync(join(dir, "default.json"), JSON.stringify(fixture, null, 2));
});
afterAll(() => rmSync(dir, { recursive: true, force: true }));

// A mounted-file source pointed at the fixture's directory. Throws rather than
// returning null so callers get a real SkinSource without a non-null cast.
function mountedSource(): SkinSource {
	const source = fileSkinSource({
		FRONTEND_SKIN_DIR: dir,
	} as unknown as NodeJS.ProcessEnv);
	if (!source) throw new Error("no file source for the mounted fixture");
	return source;
}

describe("a mounted skin descriptor", () => {
	// The resolver caches per host; clear between tests or one sees another's.
	beforeEach(() => clearSkinCache());

	it("resolves from its mounted default.json", async () => {
		const skin = await resolveSkin({
			fallback,
			host: null,
			sources: [mountedSource()],
		});
		// source === "file" proves the descriptor validated (a rejected one would
		// leave source === "default").
		expect(skin.source).toBe("file");
		expect(skin.appearance.defaultTheme).toBe("light");
		expect(skin.appearance.radius).toBe("1rem");
		expect(skin.appearance.light.primary).toBe("oklch(0.68 0.19 45)");
		expect(skin.appearance.fontHeading).toContain("Georgia");
		expect(skin.branding.name).toBe("Acme");
		expect(skin.branding.title).toBe("Acme Console");
		expect(skin.branding.logo?.lightSrc).toBe(
			"https://cdn.example.com/acme-logo.svg",
		);
		expect(skin.appearance.typeScale["7"]).toBe("1.625rem");
		expect(skin.appearance.typeRoles["page-title"].weight).toBe("600");
		expect(skin.appearance.typeSlots["card-description"]).toBe("menu-item");
		expect(skin.appearance.controlSizes.default.height).toBe("9");
		// A step the skin moved reaches every role that points at it...
		expect(resolveTypeSlot(skin.appearance, "page-title").fontSize).toBe(
			"1.625rem",
		);
		// ...and a role it did not restate keeps the compiled default.
		expect(resolveTypeSlot(skin.appearance, "section-title").fontSize).toBe(
			"1.125rem",
		);
		// Layer 4 is data a checker reads and never reaches CSS.
		expect(skin.rules.slots?.["page-title"]?.maxPerPage).toBe(1);
		expect(skin.rules.headingOrder).toBe("no-skip");
	});

	// Value assertions cover the handful of fields a human thought to name. The
	// exported survival check walks EVERY declared leaf through the real
	// mounted-file source and fails on any one that did not reach the render —
	// the same guarantee a descriptor-owning repository gets by depending on the
	// contract instead of hand-rolling it.
	it("survives resolution leaf for leaf, from the file source", async () => {
		const skin = await assertSkinSurvives(fixture, {
			fallback,
			sources: [mountedSource()],
			expectSource: "file",
		});
		expect(skin.source).toBe("file");
	});
});
