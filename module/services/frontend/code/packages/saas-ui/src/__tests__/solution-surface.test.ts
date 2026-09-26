import { describe, expect, it } from "vitest";
import * as root from "../index.js";
import * as solution from "../solution/index.js";

// The README presents the solution helpers as reachable from the package root
// AND from `@codefly-dev/saas-ui/solution`. A root that re-exported only some of
// them made that false, and split a documented pair: `NoReadableCollection` was
// on the root while the `useAccessibleScope` whose "none" answer it renders was
// not, so a consumer following the README failed to compile on one import and
// not the other.
//
// Type-only exports leave no trace in a namespace object, so this pins the value
// exports — which are exactly what a consumer's `import { … }` resolves at
// runtime, and the half that fails loudly rather than at typecheck.
describe("the solution surface", () => {
	it("is reachable by the same names from the package root", () => {
		expect(Object.keys(solution).length).toBeGreaterThan(0);
		for (const name of Object.keys(solution))
			expect(
				root,
				`@codefly-dev/saas-ui does not re-export ${name}`,
			).toHaveProperty(name);
	});
});
