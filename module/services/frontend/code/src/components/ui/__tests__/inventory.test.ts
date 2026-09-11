import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import inventory from "../../../../ui-inventory.json";

function components(directory: string): string[] {
	return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
		const file = join(directory, entry.name);
		if (entry.name === "__tests__") return [];
		if (entry.isDirectory()) return components(file);
		return file.endsWith(".tsx") && !file.includes(".test.") ? [file] : [];
	});
}

it("accounts for every host and owner React source family", () => {
	const actual = [
		"src/components",
		"src/shared",
		"src/features",
		"src/app",
		"packages/codefly-ui/src",
		"packages/saas-ui/src",
	]
		.flatMap(components)
		.sort();
	expect(inventory.families.map((entry) => entry.source).sort()).toEqual(
		actual,
	);
});

describe("inventory evidence", () => {
	for (const family of inventory.families) {
		it(family.source, () => {
			expect(family.owner).not.toBe("");
			expect(family.reason).not.toBe("");
			expect([
				"already canonical",
				"promote",
				"reconcile",
				"keep feature-local",
				"remove because unused",
			]).toContain(family.disposition);
			for (const story of family.stories) {
				expect(readFileSync(story.file, "utf8")).toMatch(
					new RegExp(`export const ${story.export}\\b`),
				);
			}
			if (family.stories.length === 0) expect(family.coverage).toBe("missing");
		});
	}
});
