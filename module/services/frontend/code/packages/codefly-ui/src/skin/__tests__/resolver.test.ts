import {
	type FrontendBranding,
	resolveFrontendAppearance,
} from "@codefly/saas-plugin-contract";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CACHE_MAX_ENTRIES, clearSkinCache, resolveSkin } from "../resolver";
import type { RawSkinDescriptor, ResolvedSkinBase, SkinSource } from "../types";

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

function source(
	name: string,
	descriptor: RawSkinDescriptor | null,
): SkinSource {
	return { name, load: async () => descriptor };
}

beforeEach(() => clearSkinCache());

describe("resolveSkin", () => {
	it("returns the compiled default when no source is configured", async () => {
		const skin = await resolveSkin({ fallback, sources: [] });
		expect(skin.source).toBe("default");
		expect(skin.branding).toEqual(branding);
	});

	it("applies a valid appearance override from a source", async () => {
		const skin = await resolveSkin({
			fallback,
			host: "acme.example.com",
			sources: [
				source("http", { appearance: { light: { primary: "#123456" } } }),
			],
		});
		expect(skin.source).toBe("http");
		expect(skin.appearance.light.primary).toBe("#123456");
		// Unspecified tokens still inherit the neutral default.
		expect(skin.appearance.light.background).toBe("oklch(1 0 0)");
	});

	it("falls back to the default when a descriptor is invalid, without throwing", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const skin = await resolveSkin({
			fallback,
			host: "bad.example.com",
			sources: [
				source("http", {
					appearance: { light: { primary: "red; color: transparent" } },
				}),
			],
		});
		expect(skin.source).toBe("default");
		expect(skin.appearance.light.primary).toBe(
			fallback.appearance.light.primary,
		);
		expect(warn).toHaveBeenCalled();
		warn.mockRestore();
	});

	it("merges an appearance override onto the compiled fallback, not the contract default", async () => {
		// A fallback whose appearance differs from the bare contract default.
		const customFallback: ResolvedSkinBase = {
			appearance: resolveFrontendAppearance({
				light: { primary: "oklch(0.5 0.2 200)" },
			}),
			branding,
		};
		const skin = await resolveSkin({
			fallback: customFallback,
			host: "acme.example.com",
			sources: [source("file", { appearance: { radius: "1rem" } })],
		});
		expect(skin.appearance.radius).toBe("1rem");
		// The override never touched primary, so it must keep the compiled
		// fallback value — not reset to the contract default ("oklch(0.205 0 0)").
		expect(skin.appearance.light.primary).toBe("oklch(0.5 0.2 200)");
	});

	it("uses the first source that returns a descriptor", async () => {
		const skin = await resolveSkin({
			fallback,
			host: "acme.example.com",
			sources: [
				source("http", null),
				source("file", { appearance: { radius: "1rem" } }),
				source("env", { appearance: { radius: "0.1rem" } }),
			],
		});
		expect(skin.source).toBe("file");
		expect(skin.appearance.radius).toBe("1rem");
	});

	it("merges branding and rejects unsafe assets", async () => {
		const skin = await resolveSkin({
			fallback,
			host: "acme.example.com",
			sources: [
				source("http", {
					branding: {
						name: "Acme",
						favicon: "http://insecure.example/f.ico",
						logo: {
							lightSrc: "https://cdn.example.com/logo.svg",
							alt: "Acme",
						},
					},
				}),
			],
		});
		expect(skin.branding.name).toBe("Acme");
		// Non-https favicon is dropped; the default is kept.
		expect(skin.branding.favicon).toBe("/favicon.ico");
		expect(skin.branding.logo?.lightSrc).toBe(
			"https://cdn.example.com/logo.svg",
		);
	});

	it("caches per host within the TTL", async () => {
		let now = 1_000;
		const load = vi.fn(async () => ({ appearance: { radius: "1rem" } }));
		const flaky: SkinSource = { name: "http", load };
		const opts = {
			fallback,
			host: "acme.example.com",
			sources: [flaky],
			now: () => now,
		};
		await resolveSkin(opts);
		now += 5_000; // still inside the 30s TTL
		await resolveSkin(opts);
		expect(load).toHaveBeenCalledTimes(1);
		now += 30_000; // past the TTL
		await resolveSkin(opts);
		expect(load).toHaveBeenCalledTimes(2);
	});

	it("bounds the cache so a flood of distinct hosts cannot grow it without bound", async () => {
		const loads = new Map<string, number>();
		// Each host loads through a shared counter so eviction is observable.
		const countingSource = (): SkinSource => ({
			name: "file",
			load: async (key) => {
				const host = key.host ?? "*";
				loads.set(host, (loads.get(host) ?? 0) + 1);
				return { appearance: { radius: "1rem" } };
			},
		});
		// Fill past the cap; the earliest host is the first to be evicted.
		for (let i = 0; i < CACHE_MAX_ENTRIES + 8; i++) {
			await resolveSkin({
				fallback,
				host: `h${i}.example.com`,
				sources: [countingSource()],
			});
		}
		// h0 was evicted (not merely expired), so resolving it again reloads.
		await resolveSkin({
			fallback,
			host: "h0.example.com",
			sources: [countingSource()],
		});
		expect(loads.get("h0.example.com")).toBe(2);
	});
});
