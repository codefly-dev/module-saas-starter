import { describe, expect, it } from "vitest";

import {
	decidePublish,
	PACKAGES,
	planPublications,
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

// A published package's dependency ranges are frozen on the registry forever.
// `@codefly-dev/saas-ui@0.2.0` pinned peer `@codefly-dev/saas-sdk` at exactly
// "0.2.1"; because the SDK versions independently of the co-versioned UI kit
// (see kit-shared-version.test.ts), the SDK's next patch makes that pin
// unsatisfiable — a solution installing the current saas-ui plus the current SDK
// gets a hard npm ERESOLVE, and the only remedy is republishing saas-ui, since
// the registry copy cannot be edited. An exact pin BETWEEN two independently
// versioned published packages is therefore a defect, not a tightening.
describe("published kit packages do not exact-pin each other", () => {
	const byName = workspacesByName(process.cwd());
	const EXACT = /^\d+\.\d+\.\d+(?:[-+].*)?$/;

	for (const name of PACKAGES) {
		it(`${name} declares range (not exact) peers on sibling kit packages`, () => {
			const manifest = byName.get(name);
			expect(manifest, `missing workspace for ${name}`).toBeDefined();
			for (const field of ["dependencies", "peerDependencies"]) {
				for (const [dep, range] of Object.entries(manifest[field] ?? {})) {
					if (!PACKAGES.includes(dep)) continue;
					expect(
						EXACT.test(range),
						`${name} ${field}.${dep} = "${range}" is an exact pin on a sibling ` +
							"published package; use a range so an independent bump of that " +
							"package cannot make this published version uninstallable",
					).toBe(false);
				}
			}
		});
	}
});

// A registry publish cannot be undone, so every failure that is knowable up
// front must happen before the FIRST publish. Packing/deciding inside the
// publish loop meant an unbumped version on the last package left the earlier
// ones already published and immutable — the host then advertising singleton
// versions a solution cannot install. `planPublications` resolves all three
// first, so the throw lands with nothing published.
describe("planPublications resolves every package before publishing any", () => {
	const manifests = new Map([
		["a", { name: "a", version: "1.0.0" }],
		["b", { name: "b", version: "2.0.0" }],
		["c", { name: "c", version: "3.0.0" }],
	]);

	it("plans each package in order with its decided action", () => {
		const plan = planPublications({
			packages: ["a", "b", "c"],
			manifests,
			packPackage: (name) => ({
				path: `/tmp/${name}.tgz`,
				integrity: `sha-${name}`,
			}),
			// `b` is already on the registry with identical bytes → skip.
			readRemoteIntegrity: (name) => (name === "b" ? "sha-b" : null),
		});
		expect(plan.map((entry) => [entry.name, entry.action])).toEqual([
			["a", "publish"],
			["b", "skip"],
			["c", "publish"],
		]);
	});

	it("throws during planning — before any publish — when a later package is stale", () => {
		const packed = [];
		expect(() =>
			planPublications({
				packages: ["a", "b", "c"],
				manifests,
				packPackage: (name) => {
					packed.push(name);
					return { path: `/tmp/${name}.tgz`, integrity: `built-${name}` };
				},
				// `c` is published with DIFFERENT bytes: an unbumped version.
				readRemoteIntegrity: (name) =>
					name === "c" ? "registry-different" : null,
			}),
		).toThrow(/c@3\.0\.0 is already published with different contents/);
		// It reached `c` (so the failure is genuinely discovered during planning),
		// and planning never publishes — the caller's publish loop never ran.
		expect(packed).toEqual(["a", "b", "c"]);
	});

	it("throws before planning anything when a package has no workspace", () => {
		expect(() =>
			planPublications({
				packages: ["a", "missing"],
				manifests,
				packPackage: (name) => ({
					path: `/tmp/${name}.tgz`,
					integrity: `x-${name}`,
				}),
				readRemoteIntegrity: () => null,
			}),
		).toThrow(/no workspace package named 'missing'/);
	});
});
