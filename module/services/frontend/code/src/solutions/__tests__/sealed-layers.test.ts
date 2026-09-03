import { createInstance } from "@module-federation/runtime";
import { describe, expect, it } from "vitest";
import { SEALED_SHARED } from "../SolutionOutlet";

// Sealing invariant (packages/codefly-ui/ARCHITECTURE.md, "Sealed downward"):
// a higher layer composes what a lower layer ships but can never shadow or
// replace it. The host publishes each sealed layer package — React, the kit,
// and each module UI package — as a Module-Federation `singleton` with
// `requiredVersion: false`, so a solution remote renders against the host's one
// true instance and a copy the remote bundles itself can never win.

const SEALED_PACKAGES = Object.keys(SEALED_SHARED) as Array<
	keyof typeof SEALED_SHARED
>;

// The config half of the seal. `singleton: true` keeps exactly one instance
// across the host and every remote; `requiredVersion: false` makes a remote
// consume the host's instance without version negotiation. A layer package
// added to the shared set without both flags splits the instance the seal
// exists to keep single, so every entry is asserted here.
describe("every sealed layer package is a host-wins singleton", () => {
	for (const pkg of SEALED_PACKAGES) {
		it(`${pkg} declares singleton: true, requiredVersion: false`, () => {
			expect(SEALED_SHARED[pkg].shareConfig).toMatchObject({
				singleton: true,
				requiredVersion: false,
			});
		});
	}
});

// The behavioral half: with the real share config, a remote that contributes
// its own competing copy of a sealed package into the shared scope still
// resolves to the host's already-loaded instance — it cannot override it. Uses
// a synthetic scope key per package so the check exercises the shared config
// without touching the process-global scope the real packages register into.
describe("a remote cannot override a sealed package", () => {
	for (const pkg of SEALED_PACKAGES) {
		it(`${pkg}: a remote's own copy resolves to the host's`, async () => {
			const key = `sealed-layers-test:${pkg}`;
			const hostInstance = { sealedOwner: "host" };
			const remoteInstance = { sealedOwner: "remote" };
			const { shareConfig } = SEALED_SHARED[pkg];

			// The host publishes and loads its one true instance, exactly as the
			// portal shell does before any solution remote mounts.
			const host = createInstance({
				name: `sealed-host-${pkg}`,
				remotes: [],
				shared: {
					[key]: {
						version: "1.0.0",
						lib: () => hostInstance,
						shareConfig,
					},
				},
			});
			await host.loadShare(key);

			// A consuming remote contributes its own copy at a strictly higher
			// version into the shared scope — the strongest attempt to override.
			host.registerShared({
				[key]: {
					version: "99.0.0",
					get: async () => () => remoteInstance,
					shareConfig,
				},
			});

			const resolved = await host.loadShare<typeof hostInstance>(key);
			const instance = typeof resolved === "function" ? resolved() : undefined;

			expect(instance?.sealedOwner).toBe("host");
		});
	}
});
