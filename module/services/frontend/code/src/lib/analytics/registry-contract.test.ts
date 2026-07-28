import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { browserEventRegistry } from "./browser";

type CanonicalRegistry = {
	defaults: { property_type: string };
	property_types: Record<string, string>;
	events: {
		name: string;
		sources: string[];
		purpose: string;
		properties: string[];
	}[];
};

describe("browser analytics registry contract", () => {
	it("matches the canonical server registry web subset", () => {
		const registry = JSON.parse(
			fs.readFileSync(
				path.resolve(
					process.cwd(),
					"../../accounts/code/pkg/analytics/registry.json",
				),
				"utf8",
			),
		) as CanonicalRegistry;

		for (const [name, browser] of Object.entries(browserEventRegistry)) {
			const canonical = registry.events.find((event) => event.name === name);
			const browserProperties = Object.fromEntries(
				Object.entries(browser.properties),
			);
			expect(
				canonical,
				`${name} must be in the canonical registry`,
			).toBeDefined();
			expect(canonical?.sources).toContain("web");
			expect(browser.purpose).toBe(canonical?.purpose);
			expect(Object.keys(browserProperties).sort()).toEqual(
				[...(canonical?.properties ?? [])].sort(),
			);
			for (const property of canonical?.properties ?? []) {
				expect(browserProperties[property]).toBe(
					registry.property_types[property] ?? registry.defaults.property_type,
				);
			}
		}
	});
});
