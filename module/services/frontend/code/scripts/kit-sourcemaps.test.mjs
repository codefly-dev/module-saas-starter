import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, posix, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { PACKAGES } from "./publish-frontend-kit.mjs";

const CODE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// A sourcemap is only useful if the sources it names can be found. tsc writes
// `sources` relative to the emitted file, pointing back into the source tree —
// which `files: ["dist", …]` does not ship. The published kit therefore carried
// maps resolving to paths absent from the tarball: every kit frame in a
// consumer's devtools read "source not found", and `.d.ts.map` broke
// go-to-definition into the package.
//
// `npm pack --dry-run --json` reports exactly the file list npm would publish,
// so this asserts the property that matters — nothing in the tarball points at
// something outside it — rather than any one fix for it.
//
// An earlier revision of this file derived its assertions solely from the maps
// present in the pack, so an UNBUILT package produced zero maps, zero
// assertions, and a green result: deleting `packages/saas-ui/dist` dropped the
// run from 124 to 108 assertions and still reported PASS. Its "sanity" check
// asserted `files.length > 0`, which README.md + package.json satisfy
// unconditionally. Hence `shipsBuiltOutput` below: the guard now proves it
// actually inspected build output before concluding anything.
// Packing is done lazily and memoized, INSIDE the tests rather than at module
// scope: a pack failure then surfaces as a named assertion instead of a bare
// collection error with no indication of which package or check was involved.
const packCache = new Map();
function packedFiles(name) {
	if (!packCache.has(name)) {
		const output = execFileSync(
			"npm",
			["pack", "--workspace", name, "--dry-run", "--json"],
			{ cwd: CODE_ROOT, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
		);
		const [entry] = JSON.parse(output);
		packCache.set(
			name,
			entry.files.map((file) => file.path.replaceAll("\\", "/")),
		);
	}
	return packCache.get(name);
}

// Workspace directory for a package, read from the manifests on disk rather
// than derived from the package name — `@codefly-dev/ui` lives in
// `packages/codefly-ui`, so name-to-directory is not a mechanical transform.
const WORKSPACE_DIRS = (() => {
	const packagesRoot = join(CODE_ROOT, "packages");
	const dirs = new Map();
	for (const entry of readdirSync(packagesRoot, { withFileTypes: true })) {
		if (!entry.isDirectory()) continue;
		try {
			const manifest = JSON.parse(
				readFileSync(join(packagesRoot, entry.name, "package.json"), "utf8"),
			);
			dirs.set(manifest.name, join(packagesRoot, entry.name));
		} catch {
			// Not a workspace package.
		}
	}
	return dirs;
})();

function packageDir(name) {
	const dir = WORKSPACE_DIRS.get(name);
	if (dir === undefined) throw new Error(`no workspace directory for ${name}`);
	return dir;
}

const SOURCE_MAPPING_URL = /\/\/# sourceMappingURL=(.+)\s*$/;

describe.each(PACKAGES)("%s ships a self-contained tarball", (name) => {
	const dir = packageDir(name);

	// Without this the two checks below are vacuously true for an unbuilt
	// package, which is precisely how the earlier revision went green.
	it("ships built output, so the checks below actually inspect something", () => {
		const files = packedFiles(name);
		const dist = files.filter((file) => file.startsWith("dist/"));
		expect(
			dist.length,
			`${name} packed no dist/ files — the package is not built, so the ` +
				"sourcemap assertions would inspect nothing and pass vacuously",
		).toBeGreaterThan(0);
	});

	it("never references a sourcemap it does not ship", () => {
		const files = packedFiles(name);
		const shipped = new Set(files);
		const dangling = [];
		for (const file of files) {
			if (!file.endsWith(".js") && !file.endsWith(".d.ts")) continue;
			const match = SOURCE_MAPPING_URL.exec(
				readFileSync(join(dir, file), "utf8"),
			);
			if (match === null) continue;
			const target = posix.normalize(
				posix.join(posix.dirname(file), match[1].trim()),
			);
			if (!shipped.has(target)) dangling.push(`${file} → ${target}`);
		}
		expect(
			dangling,
			`${name} emits sourceMappingURL comments pointing at files it does not ` +
				"publish; a consumer's devtools requests them and gets a 404",
		).toEqual([]);
	});

	it("never names a source it does not ship or inline", () => {
		const files = packedFiles(name);
		const shipped = new Set(files);
		const unresolved = [];
		for (const map of files.filter((file) => file.endsWith(".map"))) {
			const parsed = JSON.parse(readFileSync(join(dir, map), "utf8"));
			const sources = parsed.sources ?? [];
			const contents = parsed.sourcesContent ?? [];
			sources.forEach((source, index) => {
				// Either the source text is embedded in the map itself…
				if (typeof contents[index] === "string" && contents[index].length > 0)
					return;
				// …or the file it points at must be inside the published tarball.
				const target = posix.normalize(posix.join(posix.dirname(map), source));
				if (!shipped.has(target)) unresolved.push(`${map} → ${target}`);
			});
		}
		expect(
			unresolved,
			`${name} ships maps naming sources that are neither in the tarball nor ` +
				"inlined — a consumer gets 'source not found' on those frames",
		).toEqual([]);
	});
});

// `npm pack` runs prepack/prepare/prepublishOnly before building the tarball. If
// any kit package gained such a script, the three packs above would each trigger
// a BUILD — concurrently with the ~200 other files in this vitest project that
// import these packages, rewriting `dist` out from under them mid-resolution.
// That is not hypothetical: clearing `dist` from a sibling spec's `beforeAll`
// did exactly that here and failed four test files with Vite
// `resolvePackageEntry`. Today no kit package declares one, which is the only
// reason packing is safe from inside the test run — so pin that rather than rely
// on it staying true by accident.
describe("packing a kit package cannot trigger a build", () => {
	const packagesRoot = join(CODE_ROOT, "packages");
	for (const name of PACKAGES) {
		it(`${name} declares no pack lifecycle script`, () => {
			const dir = packageDir(name);
			const manifest = JSON.parse(
				readFileSync(join(dir, "package.json"), "utf8"),
			);
			const scripts = manifest.scripts ?? {};
			const lifecycle = [
				"prepack",
				"prepare",
				"prepublishOnly",
				"postpack",
			].filter((hook) => scripts[hook] !== undefined);
			expect(
				lifecycle,
				`${name} declares ${lifecycle.join(", ")}; npm pack would run it during ` +
					"the test run and rebuild dist while concurrent specs import this package",
			).toEqual([]);
		});
	}
	it("resolved a packages root (guards the loop against an empty set)", () => {
		expect(packagesRoot).toContain("packages");
		expect(PACKAGES.length).toBeGreaterThanOrEqual(3);
	});
});
