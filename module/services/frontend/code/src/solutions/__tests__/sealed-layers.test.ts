import { readFileSync } from "node:fs";
import * as Layout from "@codefly-dev/ui/layout";
import { createInstance } from "@module-federation/runtime";
import { describe, expect, it } from "vitest";
import { PACKAGES } from "../../../scripts/publish-frontend-kit.mjs";
import { SEALED_SHARED } from "../SolutionOutlet";

// Sealing invariant (packages/codefly-ui/ARCHITECTURE.md, "Sealed downward"):
// a higher layer composes what a lower layer ships but cannot shadow or replace
// it. The host seals every layer package — React, the kit, and each module UI
// package — by sharing it into the Module-Federation scope as a `singleton`, so
// exactly one instance exists across the host and every remote and a solution
// renders against the host's copy rather than one it bundles itself.
//
// `singleton: true` is the enforceable half and is asserted here directly. It is
// the flag that, if dropped, lets two copies of a layer coexist and split React
// context and the skin — so a package added to the shared set without it fails
// this test. (`requiredVersion: false` records that the host imposes no version
// floor; it does not change which instance wins, so it is checked here for
// consistency but is not itself the seal. A runtime resolution assertion is
// deliberately NOT attempted: which shared instance @module-federation/runtime
// hands back is decided by load order and by each remote's own build-time share
// config, not reproducible from the host's config object alone, so any such
// unit-level check passes regardless of these flags and would prove nothing.)

const SEALED_PACKAGES = Object.keys(SEALED_SHARED) as Array<
	keyof typeof SEALED_SHARED
>;

describe("every sealed layer package is a singleton", () => {
	for (const pkg of SEALED_PACKAGES) {
		it(`${pkg} declares singleton: true, requiredVersion: false`, () => {
			expect(SEALED_SHARED[pkg].shareConfig).toMatchObject({
				singleton: true,
				requiredVersion: false,
			});
		});
	}
});

// React (+ react-dom + jsx-runtime) and the kit + module UI packages are all
// sealed. This pins the membership so a new layer package cannot ship shared
// without the singleton flag above simply by being left out of the set.
describe("the sealed set covers React, the kit, and each module UI package", () => {
	it("includes React and its runtime subpaths", () => {
		expect(SEALED_PACKAGES).toEqual(
			expect.arrayContaining(["react", "react-dom", "react/jsx-runtime"]),
		);
	});

	it("includes the kit and module UI packages", () => {
		expect(SEALED_PACKAGES).toEqual(
			expect.arrayContaining([
				"@codefly-dev/ui",
				"@codefly-dev/saas-ui",
				"@codefly-dev/saas-sdk",
			]),
		);
	});
});

// Every Codefly package the host seals must be one the release actually
// publishes. A share key is matched by exact string, so a key naming a package
// nobody can install is inert: no remote can ever ask for it, because a remote
// can only declare a share for a dependency it resolved at its own build time.
//
// This replaces a compat alias for the pre-rename `@codefly/saas-ui` key. That
// alias was added on the theory that remotes built before the scope rename still
// ask for the old name — but `@codefly/saas-ui` was never published anywhere
// (404 on npm; GitHub Packages only accepts the org's `@codefly-dev` scope), the
// host declares `remotes: []`, and no in-repo build produces a federated remote.
// So no bundle can hold that key, and the alias defended nothing while carrying a
// deletion condition that could never be observed to be met. Asserting the
// publishable invariant keeps a dead key from being added back.
describe("every sealed Codefly package is one the release publishes", () => {
	const codeflyKeys = SEALED_PACKAGES.filter((pkg) =>
		pkg.startsWith("@codefly"),
	);

	it("seals at least the kit (guards against an empty filter passing vacuously)", () => {
		expect(codeflyKeys.length).toBeGreaterThanOrEqual(3);
	});

	for (const pkg of SEALED_PACKAGES.filter((key) =>
		key.startsWith("@codefly"),
	)) {
		it(`${pkg} is in the published set`, () => {
			expect(
				PACKAGES,
				`${pkg} is shared as a singleton but is not published, so no remote can ` +
					"install it and ask for that share key",
			).toContain(pkg.split("/").slice(0, 2).join("/"));
			if (pkg.startsWith("@codefly-dev/ui/")) {
				const manifest = JSON.parse(
					readFileSync("packages/codefly-ui/package.json", "utf8"),
				);
				expect(manifest.exports).toHaveProperty(
					`./${pkg.split("/").slice(2).join("/")}`,
				);
			}
		});
	}
});

it("shares every exported kit subpath", () => {
	const manifest = JSON.parse(
		readFileSync("packages/codefly-ui/package.json", "utf8"),
	);
	for (const path of Object.keys(manifest.exports)) {
		expect(SEALED_PACKAGES).toContain(
			path === "." ? "@codefly-dev/ui" : `@codefly-dev/ui${path.slice(1)}`,
		);
	}
});

it("a generic consumer resolves the loaded host layout singleton", async () => {
	const key = "@codefly-dev/ui/layout";
	const entry = SEALED_SHARED[key];
	const host = createInstance({
		name: "example_ui_host",
		remotes: [],
		shared: { [key]: entry },
	});
	const hostFactory = await host.loadShare<typeof Layout>(key);
	expect(hostFactory && hostFactory()).toBe(Layout);
	const consumer = createInstance({
		name: "example_ui_consumer",
		remotes: [],
		shared: {
			[key]: {
				version: entry.version,
				shareConfig: entry.shareConfig,
				lib: () => {
					throw new Error("A consumer must use the loaded host singleton");
				},
			},
		},
	});
	consumer.initShareScopeMap("default", host.shareScopeMap.default);
	const consumerFactory = await consumer.loadShare<typeof Layout>(key);
	expect(consumerFactory && consumerFactory()).toBe(Layout);
});
