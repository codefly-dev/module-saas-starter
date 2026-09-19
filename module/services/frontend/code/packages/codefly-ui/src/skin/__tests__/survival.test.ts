import {
	type FrontendBranding,
	resolveFrontendAppearance,
} from "@codefly/saas-plugin-contract";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { clearSkinCache } from "../resolver.js";
import { assertSkinSurvives, checkSkinSurvival } from "../survival.js";
import type { RawSkinDescriptor, ResolvedSkinBase } from "../types.js";

// A compiled default that shares no value with the descriptors below, so a leaf
// can only "survive" because the descriptor really applied.
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

const clean: RawSkinDescriptor = {
	appearance: {
		defaultTheme: "dark",
		radius: "0",
		light: { primary: "oklch(0.52 0.22 285)" },
		dark: { primary: "oklch(0.62 0.22 285)" },
	},
	branding: {
		name: "Acme",
		logo: { lightSrc: "/brand/acme.svg", alt: "Acme" },
	},
};

beforeEach(() => {
	clearSkinCache();
	// The resolver reports a rejected descriptor on the console; the rejection is
	// what several of these cases assert, so keep the suite output readable.
	vi.spyOn(console, "warn").mockImplementation(() => {});
	vi.spyOn(console, "error").mockImplementation(() => {});
});

describe("checkSkinSurvival", () => {
	it("reports a clean descriptor as fully surviving from its own source", async () => {
		const report = await checkSkinSurvival(clean, {
			fallback,
			expectSource: "descriptor",
		});
		expect(report.mismatches).toEqual([]);
		expect(report.source).toBe("descriptor");
		expect(report.ok).toBe(true);
		expect(report.skin.appearance.light.primary).toBe("oklch(0.52 0.22 285)");
	});

	// The failure this check exists for: the contract is fail-closed and the
	// resolver is fail-safe, so ONE unknown key costs every value the descriptor
	// carried and the render is the stock default.
	it("reports every leaf as lost when one unknown key rejects the descriptor", async () => {
		const report = await checkSkinSurvival(
			{
				...clean,
				appearance: {
					...clean.appearance,
					buttonRadius: "8px",
				} as RawSkinDescriptor["appearance"],
			},
			{ fallback, expectSource: "descriptor" },
		);
		expect(report.source).toBe("default");
		expect(report.ok).toBe(false);
		expect(report.mismatches.map((mismatch) => mismatch.path)).toEqual(
			expect.arrayContaining([
				"appearance.buttonRadius",
				"appearance.defaultTheme",
				"appearance.radius",
				"appearance.light.primary",
				"appearance.dark.primary",
			]),
		);
	});

	// A branding asset is dropped by the allowlist rather than by the validator, so
	// the rest of the descriptor still applies. The report is at `branding.logo`,
	// not `branding.logo.lightSrc`, because the resolver keeps a logo only when its
	// light source passes: an unsafe `lightSrc` costs the alt text and the dark
	// variant too. That is worth seeing, and only a real resolution shows it.
	it("reports the whole logo when its light source fails the allowlist", async () => {
		const report = await checkSkinSurvival(
			{
				...clean,
				branding: {
					name: "Acme",
					logo: { lightSrc: "data:image/svg+xml,<svg/>", alt: "Acme" },
				},
			},
			{ fallback, expectSource: "descriptor" },
		);
		expect(report.source).toBe("descriptor");
		expect(report.mismatches.map((mismatch) => mismatch.path)).toEqual([
			"branding.logo",
		]);
		expect(report.skin.branding.name).toBe("Acme");
		expect(report.skin.branding.logo).toBeUndefined();
	});

	// A top-level key beside `appearance`/`branding` is never read — nothing
	// validates it, so without this it would look accepted.
	// `remotes` was a declared-but-never-resolved field until it was removed. A
	// descriptor carrying it validated cleanly while the value went nowhere, so
	// the check has to report any top-level key the resolver does not read. The
	// cast is the case under test: the type no longer admits the key.
	it("reports a top-level key the resolver does not read", async () => {
		const report = await checkSkinSurvival(
			{
				...clean,
				remotes: { checkout: "https://example.com/remote.js" },
			} as RawSkinDescriptor,
			{ fallback },
		);
		expect(report.mismatches.map((mismatch) => mismatch.path)).toEqual([
			"remotes",
		]);
	});

	// An empty descriptor is VALID — a skin is partial by design — so it resolves
	// from its source with nothing lost. Declaring nothing is not a failure.
	it("passes an empty descriptor, which declares nothing to lose", async () => {
		const report = await checkSkinSurvival(
			{},
			{ fallback, expectSource: "descriptor" },
		);
		expect(report.mismatches).toEqual([]);
		expect(report.source).toBe("descriptor");
		expect(report.ok).toBe(true);
	});

	// The case `expectSource` is really for: the deployment's own source chain
	// found nothing, so the product IS the compiled default. No leaf can report
	// that — the descriptor never reached the resolver — and it is exactly the
	// silent swap an authoring repository must fail on.
	it("fails when the caller's own source chain found nothing", async () => {
		const report = await checkSkinSurvival(clean, {
			fallback,
			sources: [{ name: "file", load: async () => null }],
			expectSource: "file",
		});
		expect(report.source).toBe("default");
		expect(report.ok).toBe(false);
		expect(report.mismatches.length).toBeGreaterThan(0);
	});

	// Resolving through the caller's real chain is the stronger check: it proves
	// the file was found, parsed and applied, not merely that its bytes would
	// validate if something loaded them.
	it("resolves through the caller's own source chain when given one", async () => {
		const report = await checkSkinSurvival(clean, {
			fallback,
			sources: [{ name: "file", load: async () => clean }],
			expectSource: "file",
		});
		expect(report.source).toBe("file");
		expect(report.ok).toBe(true);
	});
});

describe("assertSkinSurvives", () => {
	it("returns the resolved skin when everything survives", async () => {
		const skin = await assertSkinSurvives(clean, {
			fallback,
			expectSource: "descriptor",
		});
		expect(skin.branding.name).toBe("Acme");
	});

	it("throws naming every lost path and the winning source", async () => {
		await expect(
			assertSkinSurvives(
				{
					appearance: {
						radius: "0",
						buttonHeight: "32px",
					} as RawSkinDescriptor["appearance"],
				},
				{ fallback, expectSource: "descriptor" },
			),
		).rejects.toThrow(
			/buttonHeight[\s\S]*appearance\.radius|appearance\.radius[\s\S]*buttonHeight/,
		);
	});

	it("names the rejected-to-default case explicitly", async () => {
		await expect(
			assertSkinSurvives(
				{
					appearance: {
						radius: "definitely-not-a-length",
					} as RawSkinDescriptor["appearance"],
				},
				{ fallback, expectSource: "descriptor" },
			),
		).rejects.toThrow(/compiled default rendered instead/);
	});
});
