import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, posix, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { PACKAGES } from "./publish-frontend-kit.mjs";

const CODE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// A sourcemap is only useful if the sources it names can actually be found. tsc
// writes `sources` relative to the emitted file, pointing back into the source
// tree — which `files: ["dist", …]` does not ship. The published kit therefore
// carried maps resolving to paths absent from the tarball: every kit frame in a
// consumer's devtools read "source not found", and `.d.ts.map` broke
// go-to-definition into the package. `npm pack --dry-run --json` reports exactly
// the file list npm would publish, so this asserts the property that matters —
// every map is resolvable from the tarball alone — rather than any one fix for
// it (`saas-ui`/`ui` ship `src`; `saas-sdk` cannot, and inlines its sources
// instead because its maps also reference the `generated` tree that
// packages/saas-sdk/test/published-surface.test.ts forbids shipping).
function packedFiles(name) {
	const output = execFileSync(
		"npm",
		["pack", "--workspace", name, "--dry-run", "--json"],
		{ cwd: CODE_ROOT, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
	);
	const [entry] = JSON.parse(output);
	return entry.files.map((file) => file.path);
}

function packageDir(name) {
	return join(
		CODE_ROOT,
		"packages",
		name.replace(/^@codefly-dev\//, "").replace(/^ui$/, "codefly-ui"),
	);
}

describe.each(PACKAGES)("%s ships resolvable sourcemaps", (name) => {
	const dir =
		name === "@codefly-dev/ui"
			? join(CODE_ROOT, "packages", "codefly-ui")
			: packageDir(name);
	const files = packedFiles(name);
	const shipped = new Set(files.map((file) => file.replaceAll("\\", "/")));
	const maps = files.filter((file) => file.endsWith(".map"));

	it("ships at least one sourcemap or none at all (sanity)", () => {
		expect(Array.isArray(files) && files.length > 0).toBe(true);
	});

	for (const map of maps) {
		it(`${map} resolves every source it names`, () => {
			const parsed = JSON.parse(readFileSync(join(dir, map), "utf8"));
			const sources = parsed.sources ?? [];
			const contents = parsed.sourcesContent ?? [];
			sources.forEach((source, index) => {
				// Either the source text is embedded in the map itself…
				if (typeof contents[index] === "string" && contents[index].length > 0)
					return;
				// …or the file it points at must be inside the published tarball.
				const resolved = posix.normalize(
					posix.join(posix.dirname(map), source),
				);
				expect(
					shipped.has(resolved),
					`${name}: ${map} names source "${source}" (${resolved}), which is not in ` +
						"the published tarball and carries no inline sourcesContent — a consumer " +
						"gets 'source not found' for this frame",
				).toBe(true);
			});
		});
	}
});
