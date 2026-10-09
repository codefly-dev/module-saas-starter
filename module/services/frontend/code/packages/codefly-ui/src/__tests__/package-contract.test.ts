import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
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
			if (manifest.name === "@codefly-dev/ui") return manifest;
		} catch {
			// Not this cwd — try the next candidate.
		}
	}
	throw new Error("could not locate the @codefly-dev/ui package.json");
}

// Same cwd-relative candidate trick as the manifest: find this package's `src`
// under either Vitest project's cwd.
function codeflyUiSrcDir(): string {
	for (const path of ["src", "packages/codefly-ui/src"]) {
		if (existsSync(join(path, "index.ts"))) return path;
	}
	throw new Error("could not locate the @codefly-dev/ui src directory");
}

function sourceFiles(dir: string): string[] {
	const out: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		if (entry.name === "__tests__") continue;
		const full = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...sourceFiles(full));
		else if (/\.tsx?$/.test(entry.name)) out.push(full);
	}
	return out;
}

const manifest = codeflyUiManifest();
const deps = manifest.dependencies ?? {};
const peers = manifest.peerDependencies ?? {};
const peersMeta = manifest.peerDependenciesMeta ?? {};
const exportsMap = manifest.exports ?? {};

// The public subpaths a consumer (host or Module-Federation remote) may import.
// Each must resolve to a built `dist/` entry with matching types, so a subpath is
// reachable and typed once published.
describe("@codefly-dev/ui public subpaths", () => {
	for (const subpath of [
		".",
		"./plugin-host",
		"./skin",
		"./dashboard",
		"./chat",
		"./layout",
		"./content",
		"./board",
		"./lifecycle",
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
// remote pull a second copy of @codefly-dev/saas-plugin-react — a second
// PluginRuntime React context — and `usePluginRuntime` breaks in that remote.
describe("@codefly-dev/ui dependency contract", () => {
	for (const shared of [
		"react",
		"@codefly-dev/saas-plugin-react",
		"@codefly-dev/saas-plugin-contract",
	]) {
		it(`declares ${shared} as a peer, not a bundled dependency`, () => {
			expect(peers).toHaveProperty(shared);
			expect(deps).not.toHaveProperty(shared);
		});
	}
});

// GitHub Packages omits optional-peer metadata. All runtime peers are now
// published in the same scope and required, so fresh installs need no bypass.
describe("@codefly-dev/ui installable plugin peers", () => {
	for (const shared of [
		"react",
		"@codefly-dev/saas-plugin-react",
		"@codefly-dev/saas-plugin-contract",
	]) {
		it(`keeps ${shared} a required peer`, () => {
			expect(peersMeta[shared]?.optional).not.toBe(true);
		});
	}
});

// Generic presentation imports must stay independent of the plugin runtime,
// even though npm installs its small published peers alongside the full kit.
describe("@codefly-dev/ui solution subpaths stay plugin-free", () => {
	const srcDir = codeflyUiSrcDir();
	for (const subpath of ["layout", "dashboard", "chat", "content", "board"]) {
		it(`./${subpath} imports no plugin package package`, () => {
			for (const file of sourceFiles(join(srcDir, subpath))) {
				const source = readFileSync(file, "utf8");
				expect(source, `${file} imports the plugin runtime`).not.toMatch(
					/from\s+["']@codefly(?:-dev)?\/saas-plugin/,
				);
			}
		});
	}
});

// The published vocabulary documents are bytes, not a build output: a consumer
// that freezes a catalog freezes the digest of the exact document it was approved
// under, so both must resolve out of `src` (which `files` ships) and exist.
describe("@codefly-dev/ui published documents", () => {
	const srcDir = codeflyUiSrcDir();
	for (const [subpath, file] of [
		["./dashboard-catalog.json", "dashboard/dashboard.catalog.generated.json"],
		[
			"./dashboard-data.schema.json",
			"dashboard/dashboard-data.schema.generated.json",
		],
	] as const) {
		it(`exports ${subpath} as a committed document`, () => {
			const entry = exportsMap[subpath] as unknown;
			expect(entry, `missing exports entry for ${subpath}`).toBe(`./src/${file}`);
			expect(existsSync(join(srcDir, file)), `${file} is missing`).toBe(true);
		});
	}
});
