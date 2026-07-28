import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("production chart data policy", () => {
	it("does not render literal numeric series as account data", () => {
		const sourceRoot = path.resolve(process.cwd(), "src");
		const productionSources = walk(sourceRoot).filter(
			(file) =>
				/\.(ts|tsx)$/.test(file) &&
				!file.includes("__tests__") &&
				!file.endsWith(".test.ts") &&
				!file.endsWith(".test.tsx"),
		);
		const literalSeries =
			/\b(?:points|series|data)\s*=\s*\{\s*\[\s*-?\d+(?:\.\d+)?(?:\s*,\s*-?\d+(?:\.\d+)?)+\s*,?\s*\]\s*\}/;
		const violations = productionSources.filter((file) =>
			literalSeries.test(fs.readFileSync(file, "utf8")),
		);
		expect(violations).toEqual([]);
	});
});

function walk(directory: string): string[] {
	return fs.readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
		const target = path.join(directory, entry.name);
		return entry.isDirectory() ? walk(target) : [target];
	});
}
