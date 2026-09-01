import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { CODEFLY_KIT_SHARED, CODEFLY_KIT_VERSION } from "../SolutionOutlet";

// The host shares @codefly-dev/ui, @codefly/saas-ui, and @codefly/saas-sdk into the
// Module-Federation scope under CODEFLY_KIT_VERSION. If a package is version-
// bumped without updating that constant, the host would under-report the shared
// version and a remote bundling the newer copy could win singleton resolution,
// splitting the instance. This pins the constant to the packages' real versions
// so the bump can't drift silently.
const KIT_PACKAGES = ["codefly-ui", "saas-ui", "saas-sdk"] as const;

function packageVersion(dir: string): string {
	// Vitest runs from the frontend `code` root (its config lives there).
	const manifestPath = join(process.cwd(), "packages", dir, "package.json");
	return JSON.parse(readFileSync(manifestPath, "utf8")).version;
}

describe("CODEFLY_KIT_VERSION", () => {
	it.each(KIT_PACKAGES)(
		"matches the published version of @codefly/%s",
		(dir) => {
			expect(packageVersion(dir)).toBe(CODEFLY_KIT_VERSION);
		},
	);
});

// Invariant 2 of the kit architecture (see packages/codefly-ui/ARCHITECTURE.md):
// every shared kit package is a Module-Federation SINGLETON, so an arbitrarily
// complex page loads exactly one copy of each kit module (and its tokens) across
// the host and every remote. Version pinning above only matters if singleton
// resolution is actually in force — drop `singleton: true` and two copies can
// coexist, splitting React context and the skin. Assert on the exported share
// config object itself, so a dropped flag on ANY package fails that package's
// own case (a source-text scrape can't distinguish whose block a match lands in).
describe("shared kit packages are singletons", () => {
	for (const pkg of Object.keys(CODEFLY_KIT_SHARED) as Array<
		keyof typeof CODEFLY_KIT_SHARED
	>) {
		it(`${pkg} declares singleton: true`, () => {
			expect(CODEFLY_KIT_SHARED[pkg].shareConfig.singleton).toBe(true);
		});
	}
});
