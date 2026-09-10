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

	// Every package the host shares as a Module-Federation singleton must be
	// installable by a solution remote, or the remote cannot mount the host's
	// instance (core-solutions' wiki hand-rolled a sources list because
	// `<DatasourcesPanel>` was shared but never published).
	it("publishes the SaaS-domain panels and the SDK the host shares", () => {
		expect(PACKAGES).toContain("@codefly-dev/saas-ui");
		expect(PACKAGES).toContain("@codefly-dev/saas-sdk");
	});

	// GitHub Packages rejects any scope but the org's, so a package named
	// outside `@codefly-dev` would fail at release time, not here.
	it("only publishes packages under the @codefly-dev scope", () => {
		for (const name of PACKAGES) {
			expect(name.startsWith("@codefly-dev/"), name).toBe(true);
		}
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
