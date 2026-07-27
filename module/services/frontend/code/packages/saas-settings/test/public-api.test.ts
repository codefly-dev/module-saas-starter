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

function declaredModuleExports(source: string): string[] {
	return [
		...source.matchAll(
			/export\s+(?:class|const|function|interface|type)\s+([A-Za-z0-9_]+)/g,
		),
	]
		.map((match) => match[1])
		.sort();
}

describe("SaaS settings public API", () => {
	it("matches the active public package declaration exactly", () => {
		const declaration = publicApi.packages["@codefly/saas-settings"];
		expect(declaration.status).toBe("active");
		expect(declaration.entrypoints).toEqual(["."]);
		expect(
			declaredModuleExports(
				readFileSync(join(packageDir, "src/index.ts"), "utf8"),
			),
		).toEqual([...declaration.entrypointExports["."]].sort());
	});

	it("publishes no undeclared deep entrypoint", () => {
		const packageJSON = JSON.parse(
			readFileSync(join(packageDir, "package.json"), "utf8"),
		) as {
			name: string;
			version: string;
			exports: Record<string, unknown>;
			dependencies?: Record<string, string>;
			peerDependencies?: Record<string, string>;
		};
		const declaration = publicApi.packages["@codefly/saas-settings"];
		expect(packageJSON.name).toBe("@codefly/saas-settings");
		expect(packageJSON.version).toBe(declaration.packageVersion);
		expect(Object.keys(packageJSON.exports)).toEqual(["."]);
		expect(packageJSON.dependencies).toBeUndefined();
		expect(packageJSON.peerDependencies).toBeUndefined();
	});

	it("contains no generated product schema or host dependency", () => {
		const source = readFileSync(join(packageDir, "src/index.ts"), "utf8");
		expect(source).not.toMatch(
			/(?:from\s+["'](?:@\/|react["'/]|next\/)|UserSettings|Warden|Mind)/,
		);
	});
});
