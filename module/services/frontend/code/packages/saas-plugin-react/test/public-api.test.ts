import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const testDir = dirname(fileURLToPath(import.meta.url));
const packageDir = join(testDir, "..");
const codeDir = join(testDir, "../../..");
const publicApi = JSON.parse(
	readFileSync(join(codeDir, "frontend-plugin-public-api.json"), "utf8"),
) as {
	packages: Record<
		string,
		{
			status: string;
			packageVersion?: string;
			entrypoints: string[];
			reservedEntrypoints?: string[];
			entrypointExports: Record<string, string[]>;
		}
	>;
};

function declaredBarrelExports(source: string): string[] {
	return [...source.matchAll(/export(?:\s+type)?\s*\{([\s\S]*?)\}\s*from/g)]
		.flatMap((match) => match[1].split(","))
		.map((name) => name.trim())
		.filter(Boolean)
		.sort();
}

function declaredModuleExports(source: string): string[] {
	return [
		...declaredBarrelExports(source),
		...[
			...source.matchAll(
				/export\s+(?:class|const|function|interface|type)\s+([A-Za-z0-9_]+)/g,
			),
		].map((match) => match[1]),
	].sort();
}

describe("public React plugin import map", () => {
	it("matches the active package barrel exactly", () => {
		const declaration = publicApi.packages["@codefly/saas-plugin-react"];
		expect(declaration.status).toBe("active");
		expect(declaration.entrypoints).toEqual([".", "./runtime", "./ui"]);
		expect(declaration.reservedEntrypoints).toBeUndefined();
		expect(
			declaredBarrelExports(
				readFileSync(join(packageDir, "src/index.ts"), "utf8"),
			),
		).toEqual([...declaration.entrypointExports["."]].sort());
		expect(
			declaredModuleExports(
				readFileSync(join(packageDir, "src/runtime.tsx"), "utf8"),
			),
		).toEqual([...declaration.entrypointExports["./runtime"]].sort());
		expect(
			declaredModuleExports(
				readFileSync(join(packageDir, "src/ui.tsx"), "utf8"),
			),
		).toEqual([...declaration.entrypointExports["./ui"]].sort());
	});

	it("publishes only the declared React entrypoints", () => {
		const packageJSON = JSON.parse(
			readFileSync(join(packageDir, "package.json"), "utf8"),
		) as {
			name: string;
			version: string;
			exports: Record<string, unknown>;
		};
		const declaration = publicApi.packages["@codefly/saas-plugin-react"];
		expect(packageJSON.name).toBe("@codefly/saas-plugin-react");
		expect(packageJSON.version).toBe(declaration.packageVersion);
		expect(Object.keys(packageJSON.exports)).toEqual([
			".",
			"./runtime",
			"./ui",
		]);
	});

	it("contains no product or starter-private dependency", () => {
		const source = [
			"availability.ts",
			"composition.ts",
			"index.ts",
			"runtime.tsx",
			"ui.tsx",
		]
			.map((name) => readFileSync(join(packageDir, "src", name), "utf8"))
			.join("\n");
		expect(source).not.toMatch(
			/(?:from\s+["']@\/|next\/|token-store|Warden|Mind)/,
		);
	});
});
