import { describe, expect, it } from "vitest";

import { PACKAGES, workspacesByName } from "./publish-frontend-kit.mjs";

describe("frontend kit publish set", () => {
	it("publishes @codefly/ui — the solution-facing kit", () => {
		expect(PACKAGES).toContain("@codefly/ui");
	});

	it("only publishes packages that exist as workspaces", () => {
		const byName = workspacesByName(process.cwd());
		for (const name of PACKAGES) {
			expect(byName.has(name), `missing workspace ${name}`).toBe(true);
			expect(typeof byName.get(name).version).toBe("string");
		}
	});
});
