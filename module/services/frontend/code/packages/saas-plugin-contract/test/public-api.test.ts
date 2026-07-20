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

describe("frozen public import map", () => {
	it("matches the active package barrel exactly", () => {
		const declaration = publicApi.packages["@codefly/saas-plugin-contract"];
		expect(declaration.status).toBe("active");
		expect(declaration.entrypoints).toEqual([".", "./capabilities"]);
		expect(
			declaredBarrelExports(
				readFileSync(join(packageDir, "src/index.ts"), "utf8"),
			),
		).toEqual([...declaration.entrypointExports["."]].sort());
		expect(
			declaredModuleExports(
				readFileSync(join(packageDir, "src/capabilities.ts"), "utf8"),
			),
		).toEqual([...declaration.entrypointExports["./capabilities"]].sort());
	});

	it("publishes no undeclared deep entrypoints", () => {
		const packageJSON = JSON.parse(
			readFileSync(join(packageDir, "package.json"), "utf8"),
		) as {
			name: string;
			version: string;
			exports: Record<string, unknown>;
		};
		const declaration = publicApi.packages["@codefly/saas-plugin-contract"];
		expect(packageJSON.name).toBe("@codefly/saas-plugin-contract");
		expect(packageJSON.version).toBe(declaration.packageVersion);
		expect(Object.keys(packageJSON.exports)).toEqual([".", "./capabilities"]);
	});

	it("contains no product or private-host dependency", () => {
		const source = [
			"capabilities.ts",
			"contracts.ts",
			"composition.ts",
			"allowlist.ts",
			"appearance.ts",
			"index.ts",
		]
			.map((name) => readFileSync(join(packageDir, "src", name), "utf8"))
			.join("\n");
		expect(source).not.toMatch(
			/(?:from\s+["'](?:@\/|react["'/]|next\/)|Warden|Mind)/,
		);
		const packageJSON = JSON.parse(
			readFileSync(join(packageDir, "package.json"), "utf8"),
		) as {
			dependencies?: Record<string, string>;
			devDependencies?: Record<string, string>;
			peerDependencies?: Record<string, string>;
		};
		expect(packageJSON.dependencies?.react).toBeUndefined();
		expect(packageJSON.devDependencies?.react).toBeUndefined();
		expect(packageJSON.devDependencies?.["@types/react"]).toBeUndefined();
		expect(packageJSON.peerDependencies?.react).toBeUndefined();
	});
});
