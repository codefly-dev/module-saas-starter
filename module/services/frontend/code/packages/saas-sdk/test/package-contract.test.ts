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

// This spec runs under two Vitest projects with different cwds (the SDK's own
// node config at the package root, and the host's happy-dom `pure` project at
// the frontend code root). Locate this package's own manifest by cwd-relative
// candidates, disambiguated by name rather than trusting the first hit (the host
// root also has a package.json).
function saasSdkManifest(): Manifest {
	for (const path of ["package.json", "packages/saas-sdk/package.json"]) {
		try {
			const manifest = JSON.parse(readFileSync(path, "utf8")) as Manifest;
			if (manifest.name === "@codefly/saas-sdk") return manifest;
		} catch {
			// Not this cwd — try the next candidate.
		}
	}
	throw new Error("could not locate the @codefly/saas-sdk package.json");
}

function saasSdkSrcDir(): string {
	for (const path of ["src", "packages/saas-sdk/src"]) {
		if (existsSync(join(path, "index.ts"))) return path;
	}
	throw new Error("could not locate the @codefly/saas-sdk src directory");
}

function sourceFiles(dir: string): string[] {
	const out: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const full = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...sourceFiles(full));
		else if (/\.tsx?$/.test(entry.name)) out.push(full);
	}
	return out;
}

const manifest = saasSdkManifest();
const deps = manifest.dependencies ?? {};
const peers = manifest.peerDependencies ?? {};
const peersMeta = manifest.peerDependenciesMeta ?? {};
const exportsMap = manifest.exports ?? {};

describe("@codefly/saas-sdk public subpaths", () => {
	for (const subpath of [".", "./chat"]) {
		it(`exports ${subpath} to a typed dist entry`, () => {
			const entry = exportsMap[subpath];
			expect(entry, `missing exports entry for ${subpath}`).toBeDefined();
			expect(entry.import).toMatch(/^\.\/dist\/.*\.js$/);
			expect(entry.types).toMatch(/^\.\/dist\/.*\.d\.ts$/);
		});
	}
});

// The chat hook is the SDK's only React consumer, so `react` is an *optional*
// peer: a consumer of the `.` entry (Connect facades, data-graph tooling) can
// install and build the SDK with no React in its tree.
describe("@codefly/saas-sdk react dependency contract", () => {
	it("declares react as an optional peer, not a bundled dependency", () => {
		expect(peers).toHaveProperty("react");
		expect(peersMeta.react?.optional).toBe(true);
		expect(deps).not.toHaveProperty("react");
	});
});

// The `.` entry promises to be React-free so importing `@codefly/saas-sdk` never
// pulls React into a transport-only consumer. `src/index.ts` doesn't import from
// `./chat`, so the guarantee holds exactly as long as React stays quarantined in
// `src/chat/`. If a `react` import lands anywhere else, an accidental re-export
// from `index.ts` could leak it into every consumer's main import — a regression
// the exports map cannot see. Guard the source directly.
describe("@codefly/saas-sdk main entry stays react-free", () => {
	const srcDir = saasSdkSrcDir();
	const chatDir = join(srcDir, "chat");
	it("no source outside src/chat imports react", () => {
		for (const file of sourceFiles(srcDir)) {
			if (file.startsWith(chatDir)) continue;
			const source = readFileSync(file, "utf8");
			expect(
				source,
				`${file} imports react outside the chat subpath`,
			).not.toMatch(/from\s+["']react["']/);
		}
	});

	it("the main entry does not re-export the chat subpath", () => {
		const index = readFileSync(join(srcDir, "index.ts"), "utf8");
		expect(index).not.toMatch(/["']\.\/chat/);
	});
});
