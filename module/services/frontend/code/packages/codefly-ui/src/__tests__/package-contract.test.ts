import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

interface ExportEntry {
	types?: string;
	import?: string;
	default?: string;
}

interface Manifest {
	name?: string;
	dependencies?: Record<string, string>;
	peerDependencies?: Record<string, string>;
	peerDependenciesMeta?: Record<string, { optional?: boolean }>;
	exports?: Record<string, ExportEntry>;
}

// This spec runs under two Vitest projects with different cwds (the kit's own
// node config at the package root, and the host's happy-dom `pure` project at
// the frontend code root), and `import.meta.url` is an http: URL under Vite —
// not a file: path. So locate this package's own manifest by cwd-relative
// candidates, disambiguated by name rather than trusting the first hit (the
// host root also has a package.json).
function codeflyUiManifest(): Manifest {
	for (const path of ["package.json", "packages/codefly-ui/package.json"]) {
		try {
			const manifest = JSON.parse(readFileSync(path, "utf8")) as Manifest;
			if (manifest.name === "@codefly/ui") return manifest;
		} catch {
			// Not this cwd — try the next candidate.
		}
	}
	throw new Error("could not locate the @codefly/ui package.json");
}

const manifest = codeflyUiManifest();
const deps = manifest.dependencies ?? {};
const peers = manifest.peerDependencies ?? {};
const peersMeta = manifest.peerDependenciesMeta ?? {};
const exportsMap = manifest.exports ?? {};

// The public subpaths a consumer (host or Module-Federation remote) may import.
// Each must resolve to a built `dist/` entry with matching types, so a subpath is
// reachable and typed once published.
describe("@codefly/ui public subpaths", () => {
	for (const subpath of [
		".",
		"./plugin-host",
		"./skin",
		"./dashboard",
		"./layout",
	]) {
		it(`exports ${subpath} to a typed dist entry`, () => {
			const entry = exportsMap[subpath];
			expect(entry, `missing exports entry for ${subpath}`).toBeDefined();
			expect(entry.import).toMatch(/^\.\/dist\/.*\.js$/);
			expect(entry.types).toMatch(/^\.\/dist\/.*\.d\.ts$/);
		});
	}
});

// The kit is the dedupe surface for the host and its Module-Federation remotes.
// The stateful, context-bearing platform packages must be peers so exactly one
// instance is resolved by the consumer. Bundling them as `dependencies` lets a
// remote pull a second copy of @codefly/saas-plugin-react — a second
// PluginRuntime React context — and `usePluginRuntime` breaks in that remote.
describe("@codefly/ui dependency contract", () => {
	for (const shared of [
		"react",
		"@codefly/saas-plugin-react",
		"@codefly/saas-plugin-contract",
	]) {
		it(`declares ${shared} as a peer, not a bundled dependency`, () => {
			expect(peers).toHaveProperty(shared);
			expect(deps).not.toHaveProperty(shared);
		});
	}
});

// The solution-facing subpaths (`./layout`, `./dashboard`) are pure React
// presentation and never touch the plugin runtime. Marking the plugin packages
// optional peers lets a solution install `@codefly/ui` for those subpaths alone
// without npm auto-resolving the host-internal (unpublished) plugin packages —
// while the host, which imports `.`/`./plugin-host`/`./skin`, still provides
// them. `react` stays a required peer: every subpath needs it deduped.
describe("@codefly/ui peer-free solution surface", () => {
	for (const optional of [
		"@codefly/saas-plugin-react",
		"@codefly/saas-plugin-contract",
	]) {
		it(`marks ${optional} as an optional peer`, () => {
			expect(peersMeta[optional]?.optional).toBe(true);
		});
	}

	it("keeps react a required peer", () => {
		expect(peersMeta.react?.optional).not.toBe(true);
	});
});
