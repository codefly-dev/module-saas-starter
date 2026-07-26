import { describe, expect, it } from "vitest";

import { orderWorkspaceManifests } from "./build-plugin-workspaces.mjs";

function workspace(name, dependencies = {}) {
	return { directory: name, manifest: { name, dependencies } };
}

describe("plugin workspace build order", () => {
	it("builds local workspace dependencies before their consumers", () => {
		const ordered = orderWorkspaceManifests([
			workspace("@product/plugin", {
				"@codefly/saas-plugin-react": "0.4.1",
				"@codefly/saas-plugin-contract": "2.1.0",
			}),
			workspace("@codefly/saas-plugin-react"),
			workspace("@codefly/saas-plugin-contract"),
		]);
		expect(ordered.map(({ manifest }) => manifest.name)).toEqual([
			"@codefly/saas-plugin-contract",
			"@codefly/saas-plugin-react",
			"@product/plugin",
		]);
	});

	it("rejects duplicate names and local dependency cycles", () => {
		expect(() =>
			orderWorkspaceManifests([workspace("duplicate"), workspace("duplicate")]),
		).toThrow(/duplicate workspace package name/);
		expect(() =>
			orderWorkspaceManifests([
				workspace("a", { b: "1.0.0" }),
				workspace("b", { a: "1.0.0" }),
			]),
		).toThrow(/contains a cycle/);
	});
});
