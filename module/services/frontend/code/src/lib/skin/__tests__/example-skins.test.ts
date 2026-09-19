import { readFileSync } from "node:fs";
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
import { beforeEach, describe, expect, it } from "vitest";
import { clearSkinCache, resolveSkin } from "..";
import { fileSkinSource } from "../sources";

// A neutral fallback that shares NONE of the example skins' distinctive values,
// so any assertion below can only pass if the example file actually resolved.
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

// Vitest runs with cwd at the frontend code root; the example skins live under
// examples/skins/ there. (Resolving via import.meta.url fails under Vitest,
// which rebases relative URLs onto the Vite dev-server origin.)
function exampleDir(name: string): string {
	return join(process.cwd(), "examples", "skins", name);
}

// A mounted-file source pointed at one example skin's directory. Throws rather
// than returning null so callers get a real SkinSource without a non-null cast.
function exampleSource(name: string): SkinSource {
	const source = fileSkinSource({
		FRONTEND_SKIN_DIR: exampleDir(name),
	} as unknown as NodeJS.ProcessEnv);
	if (!source) throw new Error(`no file source for example skin '${name}'`);
	return source;
}

/**
 * Guards the shipped example skins (examples/skins/<name>/default.json) against
 * contract drift: each must resolve through the real mounted-file source +
 * resolver + contract validator, not silently fall back to the default. If a
 * token name is renamed or a value goes out of range, these break instead of a
 * deployment.
 */
describe("shipped example skins", () => {
	// The resolver caches per host; both example skins resolve under host "*",
	// so clear between tests or one test sees another's cached skin.
	beforeEach(() => clearSkinCache());

	it("resolves Helios from its mounted default.json", async () => {
		const skin = await resolveSkin({
			fallback,
			host: null,
			sources: [exampleSource("helios")],
		});
		// source === "file" proves the descriptor validated (a rejected one would
		// leave source === "default").
		expect(skin.source).toBe("file");
		expect(skin.appearance.defaultTheme).toBe("light");
		expect(skin.appearance.radius).toBe("1rem");
		expect(skin.appearance.light.primary).toBe("oklch(0.68 0.19 45)");
		expect(skin.appearance.fontHeading).toContain("Georgia");
		expect(skin.branding.name).toBe("Helios");
		expect(skin.branding.title).toBe("Helios Console");
		expect(skin.branding.logo?.lightSrc).toBe("/brand/helios-logo.svg");
		expect(skin.branding.logo?.darkSrc).toBe("/brand/helios-logo-dark.svg");
		// Layers 1 to 3: a moved scale step, a re-weighted role, a re-pointed slot
		// and a taller control rung, so the examples exercise the vocabulary rather
		// than only the colour half.
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
	});

	it("resolves Nocturne from its mounted default.json", async () => {
		const skin = await resolveSkin({
			fallback,
			host: null,
			sources: [exampleSource("nocturne")],
		});
		expect(skin.source).toBe("file");
		expect(skin.appearance.defaultTheme).toBe("dark");
		expect(skin.appearance.radius).toBe("0");
		expect(skin.appearance.dark.primary).toBe("oklch(0.62 0.22 285)");
		expect(skin.appearance.fontHeading).toContain("Courier New");
		expect(skin.branding.name).toBe("Nocturne");
		expect(skin.branding.logo?.lightSrc).toBe("/brand/nocturne-logo.svg");
		// Layer 4: rules are carried through as data and never reach CSS.
		expect(skin.rules.slots?.["page-title"]?.maxPerPage).toBe(1);
		expect(skin.rules.headingOrder).toBe("no-skip");
	});

	// Value assertions cover the handful of fields a human thought to name; the
	// example skins declare 88 tokens between them, so a rename of any of the
	// other 80 would pass every test above. The exported survival check walks
	// EVERY declared leaf through the real mounted-file source and fails on any
	// one that did not reach the render — the same guarantee a descriptor-owning
	// repository gets by depending on the contract instead of hand-rolling it.
	it.each(["helios", "nocturne"])(
		"%s survives resolution leaf for leaf, from the file source",
		async (name) => {
			const descriptor = JSON.parse(
				readFileSync(join(exampleDir(name), "default.json"), "utf8"),
			) as RawSkinDescriptor;
			const skin = await assertSkinSurvives(descriptor, {
				fallback,
				sources: [exampleSource(name)],
				expectSource: "file",
			});
			expect(skin.source).toBe("file");
		},
	);

	it("gives the two example skins genuinely different appearances", async () => {
		// Distinct hosts so the two resolutions don't share a cache entry.
		const helios = await resolveSkin({
			fallback,
			host: "helios.example",
			sources: [exampleSource("helios")],
		});
		const nocturne = await resolveSkin({
			fallback,
			host: "nocturne.example",
			sources: [exampleSource("nocturne")],
		});
		expect(helios.appearance.radius).not.toBe(nocturne.appearance.radius);
		expect(helios.appearance.defaultTheme).not.toBe(
			nocturne.appearance.defaultTheme,
		);
		expect(helios.branding.name).not.toBe(nocturne.branding.name);
	});
});
