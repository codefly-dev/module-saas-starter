import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { CODEFLY_KIT_SHARED } from "../SolutionOutlet";

// The host shares @codefly-dev/ui, @codefly/saas-ui, and @codefly-dev/saas-sdk
// into the Module-Federation scope, each under its own published version. If a
// share entry under-reports its package's real version, a remote bundling the
// newer copy could win singleton resolution and split the instance. The UI kit
// (@codefly-dev/ui + @codefly/saas-ui) is co-versioned; @codefly-dev/saas-sdk
// tracks the API contract and versions independently — so this pins EACH share
// entry's declared version to that package's actual package.json version rather
// than to one shared constant, and a bump to any of them can't drift silently.
const SHARE_KEY_TO_DIR: Record<keyof typeof CODEFLY_KIT_SHARED, string> = {
	"@codefly-dev/ui": "codefly-ui",
	"@codefly/saas-ui": "saas-ui",
	"@codefly-dev/saas-sdk": "saas-sdk",
};

function packageVersion(dir: string): string {
	// Vitest runs from the frontend `code` root (its config lives there).
	const manifestPath = join(process.cwd(), "packages", dir, "package.json");
	return JSON.parse(readFileSync(manifestPath, "utf8")).version;
}

describe("shared kit versions match their published packages", () => {
	it.each(Object.entries(SHARE_KEY_TO_DIR))(
		"%s shares its real package.json version",
		(shareKey, dir) => {
			const entry =
				CODEFLY_KIT_SHARED[shareKey as keyof typeof CODEFLY_KIT_SHARED];
			expect(entry, `missing share entry for ${shareKey}`).toBeDefined();
			expect(entry.version).toBe(packageVersion(dir));
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
