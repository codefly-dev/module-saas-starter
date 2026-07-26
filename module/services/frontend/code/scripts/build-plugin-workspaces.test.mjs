import assert from "node:assert/strict";
import { test } from "vitest";

import { orderWorkspaceManifests } from "./build-plugin-workspaces.mjs";

function workspace(name, dependencies = {}) {
	return { directory: name, manifest: { name, dependencies } };
}

test("builds local workspace dependencies before their consumers", () => {
	const ordered = orderWorkspaceManifests([
		workspace("@product/plugin", {
			"@codefly/saas-plugin-react": "0.4.1",
			"@codefly/saas-plugin-contract": "2.1.0",
		}),
		workspace("@codefly/saas-plugin-react"),
		workspace("@codefly/saas-plugin-contract"),
	]);
	assert.deepEqual(
		ordered.map(({ manifest }) => manifest.name),
		[
			"@codefly/saas-plugin-contract",
			"@codefly/saas-plugin-react",
			"@product/plugin",
		],
	);
});

test("rejects duplicate names and local dependency cycles", () => {
	assert.throws(
		() =>
			orderWorkspaceManifests([workspace("duplicate"), workspace("duplicate")]),
		/duplicate workspace package name/,
	);
	assert.throws(
		() =>
			orderWorkspaceManifests([
				workspace("a", { b: "1.0.0" }),
				workspace("b", { a: "1.0.0" }),
			]),
		/contains a cycle/,
	);
});
