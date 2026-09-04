import { describe, expect, it } from "vitest";
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
				"@codefly/saas-ui",
				"@codefly/saas-sdk",
			]),
		);
	});
});
