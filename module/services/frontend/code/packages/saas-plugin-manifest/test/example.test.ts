import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import yaml from "js-yaml";
import { describe, expect, it } from "vitest";

import {
	loadPluginManifest,
	SOLUTION_SPEC_API_VERSION,
	toSolutionSpec,
} from "../src/index.js";

const testDir = dirname(fileURLToPath(import.meta.url));
const example = yaml.load(
	readFileSync(join(testDir, "../examples/plugin.codefly.yaml"), "utf8"),
);

describe("reference plugin.codefly.yaml", () => {
	it("conforms to the manifest schema", () => {
		expect(() => loadPluginManifest(example)).not.toThrow();
	});

	it("projects onto a SolutionSpec preserving shared facts", () => {
		const manifest = loadPluginManifest(example);
		const spec = toSolutionSpec(manifest);

		expect(spec.apiVersion).toBe(SOLUTION_SPEC_API_VERSION);
		expect(spec.kind).toBe("Solution");
		expect(spec.metadata).toEqual(manifest.metadata);
		expect(spec.services).toBe(manifest.services);
		expect(spec.api).toBe(manifest.api);
		expect(spec.events).toBe(manifest.events);
		expect(spec.ui).toBe(manifest.ui);
		expect(spec.needs).toBe(manifest.needs);
		expect(spec.permissions).toBe(manifest.permissions);
		expect(spec.lifecycle).toBe(manifest.lifecycle);
	});

	it("carries starter-only sections through the x-codefly extension", () => {
		const manifest = loadPluginManifest(example);
		const extensions = toSolutionSpec(manifest).extensions?.["x-codefly"];

		expect(extensions?.entitlements).toBe(manifest.entitlements);
		expect(extensions?.config).toBe(manifest.config);
		expect(extensions?.migrations).toBe(manifest.migrations);
		expect(extensions?.egress).toBe(manifest.egress);
		expect(extensions?.integrity).toBe(manifest.integrity);
	});

	it("omits absent sections and the extension when empty", () => {
		const minimal = loadPluginManifest({
			apiVersion: "plugin.codefly.dev/v1",
			kind: "Plugin",
			metadata: { name: "minimal", version: "0.1.0" },
		});
		const spec = toSolutionSpec(minimal);

		expect(spec.services).toBeUndefined();
		expect(spec.api).toBeUndefined();
		expect(spec.extensions).toBeUndefined();
	});
});
