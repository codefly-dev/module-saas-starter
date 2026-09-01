import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { CODEFLY_KIT_VERSION } from "../SolutionOutlet";

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
