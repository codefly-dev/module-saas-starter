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
		// Reported at error level: the request rendered a different brand than the
		// descriptor asked for, which is not a warning-level event.
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
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
		expect(error).toHaveBeenCalled();
		error.mockRestore();
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
	it("applies the known tokens of a descriptor that also carries unknown keys", async () => {
		// A descriptor written against a different token vocabulary: a real
		// palette alongside keys this contract has never defined. Before the
		// descriptor was sanitized, the first unknown key discarded all of it and
		// the request rendered the stock neutral default.
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		const skin = await resolveSkin({
			fallback,
			host: "brand.example.com",
			sources: [
				source("file", {
					appearance: {
						fontSans: "Inter, Arial, sans-serif",
						buttonRadius: "0.25rem",
						light: { primary: "#0055CC", primaryHover: "#0044AA" },
					},
				} as RawSkinDescriptor),
			],
		});

		// The brand survives.
		expect(skin.source).toBe("file");
		expect(skin.appearance.fontSans).toBe("Inter, Arial, sans-serif");
		expect(skin.appearance.light.primary).toBe("#0055CC");

		// And the operator is told, by name, what did not take effect.
		expect(error).toHaveBeenCalledTimes(1);
		const message = String(error.mock.calls[0]?.[0]);
		expect(message).toContain("buttonRadius");
		expect(message).toContain("light.primaryHover");
		expect(message).toContain("brand.example.com");
		error.mockRestore();
	});

	it("says nothing when a descriptor declares only known tokens", async () => {
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		await resolveSkin({
			fallback,
			host: "clean.example.com",
			sources: [source("file", { appearance: { radius: "1rem" } })],
		});
		expect(error).not.toHaveBeenCalled();
		error.mockRestore();
	});

	it("still falls back to the compiled default when a value is unsafe", async () => {
		// Dropping unknown keys must not widen the injection gate: an unsafe
		// value is a rejection of the whole descriptor, exactly as before.
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		const skin = await resolveSkin({
			fallback,
			host: "unsafe.example.com",
			sources: [
				source("file", {
					appearance: {
						buttonRadius: "0.25rem",
						light: { primary: "red; background: url(javascript:alert(1))" },
					},
				} as RawSkinDescriptor),
			],
		});
		expect(skin.source).toBe("default");
		expect(skin.appearance.light.primary).toBe(
			fallback.appearance.light.primary,
		);
		error.mockRestore();
	});

	it("never claims dropped keys were applied to a descriptor it then rejected", async () => {
		// The report used to be emitted before validation ran, so a descriptor
		// carrying BOTH an unknown key and an out-of-range value logged "the
		// remaining tokens were applied" and then rendered the compiled default
		// anyway. Two contradictory records for one descriptor, the first of them
		// false: the brand was discarded whole.
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		const skin = await resolveSkin({
			fallback,
			host: "contradiction.example.com",
			sources: [
				source("file", {
					appearance: { buttonRadius: "0.25rem", radius: "NOT_A_LENGTH" },
				} as RawSkinDescriptor),
			],
		});

		expect(skin.source).toBe("default");
		const messages = error.mock.calls.map((call) => String(call[0]));
		expect(messages.some((message) => message.includes("rejected"))).toBe(true);
		expect(messages.every((message) => !message.includes("were applied"))).toBe(
			true,
		);
		error.mockRestore();
	});

	it("reports a descriptor shape once, not once per resolving host", async () => {
		// The report sits behind the per-host cache, and cache keys are request
		// Host headers — attacker-controllable and unbounded. Reporting per
		// resolution turns one stale descriptor into one error record per request.
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		const descriptor = {
			appearance: { buttonRadius: "0.25rem", light: { primary: "#0055CC" } },
		} as RawSkinDescriptor;
		for (let i = 0; i < 25; i++)
			await resolveSkin({
				fallback,
				host: `flood-${i}.example.com`,
				sources: [source("file", descriptor)],
			});
		expect(error).toHaveBeenCalledTimes(1);
		error.mockRestore();
	});

	it("cannot be made to forge a log record through a key name", async () => {
		// Key names are descriptor-controlled. A newline in one used to end the
		// record and start a line of the attacker's choosing.
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		await resolveSkin({
			fallback,
			host: "forge.example.com",
			sources: [
				source("file", {
					appearance: {
						"a\n[skin] descriptor accepted, all good": "x",
					},
				} as unknown as RawSkinDescriptor),
			],
		});
		const message = String(error.mock.calls[0]?.[0]);
		expect(message).not.toContain("\n");
		expect(message.split("\n")).toHaveLength(1);
		error.mockRestore();
	});

	it("caps how much of an oversized descriptor one report names", async () => {
		// 50k unknown keys used to produce a single ~390KB log record.
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		const appearance: Record<string, unknown> = {};
		for (let i = 0; i < 5_000; i++) appearance[`unknown${i}`] = "x";
		await resolveSkin({
			fallback,
			host: "oversized.example.com",
			sources: [source("file", { appearance } as unknown as RawSkinDescriptor)],
		});
		const message = String(error.mock.calls[0]?.[0]);
		expect(message).toContain("5000 unknown appearance key(s)");
		expect(message).toContain("(+4980 more)");
		expect(message.length).toBeLessThan(2_000);
		error.mockRestore();
	});
});
