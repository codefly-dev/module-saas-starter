import { describe, expect, it } from "vitest";

import {
	decidePublish,
	PACKAGES,
	workspacesByName,
} from "./publish-frontend-kit.mjs";

describe("frontend kit publish set", () => {
	it("publishes @codefly-dev/ui — the solution-facing kit", () => {
		expect(PACKAGES).toContain("@codefly-dev/ui");
	});

	it("only publishes packages that exist as workspaces", () => {
		const byName = workspacesByName(process.cwd());
		for (const name of PACKAGES) {
			expect(byName.has(name), `missing workspace ${name}`).toBe(true);
			expect(typeof byName.get(name).version).toBe("string");
		}
	});
});

describe("decidePublish", () => {
	const base = { name: "@codefly-dev/ui", version: "0.1.0" };

	it("publishes a version that is not on the registry", () => {
		expect(
			decidePublish({
				...base,
				localIntegrity: "sha512-a",
				remoteIntegrity: null,
			}),
		).toBe("publish");
	});

	it("skips a version whose published bytes are identical", () => {
		expect(
			decidePublish({
				...base,
				localIntegrity: "sha512-a",
				remoteIntegrity: "sha512-a",
			}),
		).toBe("skip");
	});

	// The regression this guards: the kit changed but the version did not, so the
	// registry would silently keep serving stale code to solutions. Fail instead.
	it("fails when the published version has different bytes", () => {
		expect(() =>
			decidePublish({
				...base,
				localIntegrity: "sha512-new",
				remoteIntegrity: "sha512-old",
			}),
		).toThrow(/bump @codefly-dev\/ui's version/);
	});
});
