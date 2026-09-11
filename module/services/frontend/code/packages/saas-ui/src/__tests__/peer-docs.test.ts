import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const packageRoot = resolve(
	dirname(fileURLToPath(import.meta.url)),
	"..",
	"..",
);

// This package declares every runtime library as a PEER, so a consumer that
// installs it without them gets no install error — npm only warns — and fails
// later at import. The README therefore carries an explicit `npm i` line, and a
// hand-maintained list of dependency names is exactly the kind of documentation
// that goes stale silently: the sibling SDK README carried a version literal
// that was wrong the moment the package was bumped.
//
// So pin the doc to the manifest in both directions rather than trusting anyone
// to update both. Add a peer without documenting it and this fails; drop a peer
// while leaving it in the README and this fails too.
describe("the README install line matches peerDependencies", () => {
	const manifest = JSON.parse(
		readFileSync(join(packageRoot, "package.json"), "utf8"),
	) as { name: string; peerDependencies?: Record<string, string> };
	const readme = readFileSync(join(packageRoot, "README.md"), "utf8");

	// The fenced block containing the install command.
	const installBlock = (() => {
		for (const block of readme
			.split("```")
			.filter((_, index) => index % 2 === 1)) {
			if (block.includes("npm i ")) return block;
		}
		throw new Error("README has no ``` block containing an `npm i ` command");
	})();

	// Compare PACKAGE names only. A shell comment or an npm flag inside the fence
	// is legitimate prose/usage, not a drifted dependency, and treating it as one
	// would fail with "the list has drifted from the manifest" — a message that
	// names the wrong cause and sends the reader to package.json for nothing.
	const documented = new Set(
		installBlock
			.split("\n")
			.map((line) => line.replace(/#.*$/, "")) // strip shell comments
			.join(" ")
			.split(/\s+/)
			.filter(
				(token) => token && token !== "\\" && token !== "npm" && token !== "i",
			)
			.filter((token) => !token.startsWith("-")) // npm flags, e.g. --save-exact
			.filter((token) => /^(?:@[\w.-]+\/)?[\w.-]+$/.test(token)),
	);
	const peers = Object.keys(manifest.peerDependencies ?? {});

	it("documents every peer the manifest declares", () => {
		expect(peers.length).toBeGreaterThan(0);
		for (const peer of peers) {
			expect(
				documented.has(peer),
				`${peer} is a peerDependency but the README install line omits it, so a ` +
					"consumer following the docs gets an unmet peer and an import-time failure",
			).toBe(true);
		}
	});

	it("documents nothing that is not a peer or the package itself", () => {
		const allowed = new Set([...peers, manifest.name]);
		for (const token of documented) {
			expect(
				allowed.has(token),
				`README install line names ${token}, which is neither this package nor ` +
					"one of its peerDependencies — the list has drifted from the manifest",
			).toBe(true);
		}
	});
});
