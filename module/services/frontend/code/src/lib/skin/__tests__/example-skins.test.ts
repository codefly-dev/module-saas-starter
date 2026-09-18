import { join } from "node:path";
import {
	type FrontendBranding,
	resolveFrontendAppearance,
} from "@codefly/saas-plugin-contract";
import type { ResolvedSkinBase, SkinSource } from "@codefly-dev/ui/skin";
import { beforeEach, describe, expect, it, vi } from "vitest";
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
 *
 * The rename half of that promise needs `expectsNoDiagnostics` below, not the
 * value assertions. A renamed token used to throw and flip `skin.source` to
 * "default", which every assertion here caught; now it is dropped and reported,
 * and the resolution still succeeds. The two examples declare 88 tokens between
 * them and the value assertions name 8, so without asserting that resolving
 * them is silent, a rename of any of the other 80 would sail through green.
 */
describe("shipped example skins", () => {
	// The resolver caches per host; both example skins resolve under host "*",
	// so clear between tests or one test sees another's cached skin.
	beforeEach(() => clearSkinCache());

	/**
	 * Fail if resolving emits any diagnostic. The resolver reports a dropped
	 * unknown key, and a rejected descriptor, at `error` level — a shipped
	 * example must produce neither.
	 */
	function expectsNoDiagnostics() {
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		return () => {
			const messages = error.mock.calls.map((call) => String(call[0]));
			error.mockRestore();
			expect(messages).toEqual([]);
		};
	}

	it("resolves Helios from its mounted default.json", async () => {
		const assertSilent = expectsNoDiagnostics();
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
		// Every token the descriptor declares is one this contract still defines.
		assertSilent();
	});

	it("resolves Nocturne from its mounted default.json", async () => {
		const assertSilent = expectsNoDiagnostics();
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
		assertSilent();
	});

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
