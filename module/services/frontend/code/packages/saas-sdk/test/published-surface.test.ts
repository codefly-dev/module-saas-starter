import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeAll, describe, expect, it } from "vitest";

// The package root (this file lives in `<root>/test`). Resolving from the module
// URL keeps the test correct regardless of the working directory vitest runs in.
const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const srcRoot = join(packageRoot, "src");
const generatedGenRoot = join(
	packageRoot,
	"generated",
	"typescript",
	"src",
	"gen",
);
// Where the freshly-emitted tree is inspected. The build below writes to a
// TEMPORARY outDir rather than the package's real `dist`, because this spec is
// collected by the frontend host's `pure` vitest project (its include covers
// `packages/**`) and therefore runs CONCURRENTLY with files that import
// `@codefly-dev/saas-sdk` and `@codefly-dev/saas-ui`. Emitting over — or, worse,
// clearing — the live `dist` mid-run deletes the package entry those files are
// resolving, and Vite fails them with `resolvePackageEntry`. A temp outDir also
// makes the assertions honest: they read only what THIS tsc invocation emitted,
// never an artifact orphaned in `dist` by an earlier config.
let distGenRoot = "";

function listFiles(dir: string): string[] {
	if (!existsSync(dir)) return [];
	const out: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const full = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...listFiles(full));
		else out.push(full);
	}
	return out;
}

function importSpecifiers(source: string): string[] {
	// Matches both `import … from "x"` and `export … from "x"`, the only forms
	// the generated bindings and `src` use to reach other modules.
	const specifiers: string[] = [];
	const re = /\bfrom\s*["']([^"']+)["']/g;
	let match = re.exec(source);
	while (match !== null) {
		specifiers.push(match[1]);
		match = re.exec(source);
	}
	return specifiers;
}

function resolveRelative(fromFile: string, specifier: string): string {
	const joined = resolve(dirname(fromFile), specifier);
	// ESM specifiers carry a `.js` extension that maps to the `.ts` source.
	if (joined.endsWith(".js")) return `${joined.slice(0, -3)}.ts`;
	if (joined.endsWith(".ts")) return joined;
	return `${joined}.ts`;
}

function toModuleKey(root: string, file: string): string {
	return relative(root, file)
		.replaceAll("\\", "/")
		.replace(/\.(ts|js)$/, "");
}

// The set of generated `_pb` modules reachable from `src` via imports — exactly
// what `tsconfig.json` (compilation root `src` only) makes tsc emit into `dist`.
// Computing it here independently of tsc lets the emitted set be checked against
// the intended closure: if the include globs the generated tree again, tsc emits
// the whole message graph and the emitted set no longer matches this closure.
function reachablePbModules(): Set<string> {
	const seen = new Set<string>();
	const queue: string[] = [];
	for (const file of listFiles(srcRoot)) {
		if (file.endsWith(".ts")) {
			seen.add(file);
			queue.push(file);
		}
	}
	for (let next = queue.pop(); next !== undefined; next = queue.pop()) {
		let source: string;
		try {
			source = readFileSync(next, "utf8");
		} catch {
			continue;
		}
		for (const specifier of importSpecifiers(source)) {
			if (!specifier.startsWith(".")) continue;
			const target = resolveRelative(next, specifier);
			if (existsSync(target) && !seen.has(target)) {
				seen.add(target);
				queue.push(target);
			}
		}
	}
	const pb = new Set<string>();
	for (const file of seen) {
		if (file.startsWith(generatedGenRoot) && file.endsWith("_pb.ts")) {
			pb.add(toModuleKey(generatedGenRoot, file));
		}
	}
	return pb;
}

function emittedPbModules(): Set<string> {
	const pb = new Set<string>();
	for (const file of listFiles(distGenRoot)) {
		if (file.endsWith("_pb.js")) pb.add(toModuleKey(distGenRoot, file));
	}
	return pb;
}

// Internal / adjacent proto surfaces that must never reach a public consumer of
// the SDK. These are message shapes for endpoints the SDK deliberately does not
// expose (admin, authz, mfa, sso, identity, delegations, privacy, billing, …);
// the generator emits them because the contract is the full package descriptor,
// but the build must strip them. Kept as an explicit denylist so the intent —
// and the security stake — is legible where the guard lives.
const FORBIDDEN_MODULES = [
	"saas/accounts/v1/platform_admin_pb",
	"saas/accounts/v1/authorization_pb",
	"saas/accounts/v1/authentication_pb",
	"saas/accounts/v1/mfa_pb",
	"saas/accounts/v1/sso_pb",
	"saas/accounts/v1/identity_pb",
	"saas/accounts/v1/delegations_pb",
	"saas/accounts/v1/privacy_pb",
	"saas/accounts/v1/billing_pb",
	"saas/accounts/v1/api_keys_pb",
	"saas/accounts/v1/introspection_pb",
	"saas/accounts/v1/consent_pb",
	"saas/accounts/v1/organizations_pb",
	"saas/accounts/v1/teams_pb",
	"saas/accounts/v1/invitations_pb",
];

// A bare list of unresolved specifiers has twice been read as a protoc-gen-es
// version difference, which it is not — both the healthy and the broken tree are
// stamped v2.2.3. Name the descriptor trees that went missing and the one thing
// worth checking, so the next failure points at the binary that produced it.
function diagnoseDangling(dangling: string[]): string | undefined {
	if (dangling.length === 0) return undefined;
	const trees = [
		...new Set(
			dangling.map((entry) =>
				entry
					.slice(entry.indexOf("-> ") + 3)
					.replace(/^(?:\.\.\/)+/, "")
					.replace(/\/[^/]+$/, ""),
			),
		),
	].sort();
	return [
		`${dangling.length} generated imports resolve to files that were never emitted.`,
		`Descriptor trees missing from the generated tree: ${trees.join(", ")}.`,
		"This is which codefly binary generated the tree, not the protoc-gen-es version:",
		"compare `codefly version` against the pin in .github/workflows/ci.yml and regenerate.",
		"See the README's 'Regenerating the client' and codefly-dev/core#438.",
	].join(" ");
}

// `codefly generate client` emits the third-party descriptors its bindings
// import relatively — `buf/validate`, `google/api`, and the `google/protobuf`
// files those two reach — alongside `saas/**`. Whether it emits them depends on
// which codefly binary ran: a core that generates from a marked descriptor set
// skips every foreign-namespace file, and the two with no package to fall back
// to (`buf/validate`, `google/api`) stay imported by relative path after the
// clean step has removed them (see README, and codefly-dev/core#438). Nothing
// else catches that: `tsconfig.json` compiles from `src` only, so tsc never
// opens a generated file the public API does not reach, and a dangling import in
// one of those is invisible until a consumer imports it.
describe("@codefly-dev/saas-sdk generated tree integrity", () => {
	it("resolves every relative import in the generated bindings", () => {
		const dangling: string[] = [];
		for (const file of listFiles(generatedGenRoot)) {
			if (!file.endsWith(".ts")) continue;
			for (const specifier of importSpecifiers(readFileSync(file, "utf8"))) {
				if (!specifier.startsWith(".")) continue;
				if (existsSync(resolveRelative(file, specifier))) continue;
				dangling.push(`${relative(generatedGenRoot, file)} -> ${specifier}`);
			}
		}
		expect(dangling, diagnoseDangling(dangling)).toEqual([]);
	});
});

describe("@codefly-dev/saas-sdk published proto surface", () => {
	// The published tarball is `dist` (`files: ["dist"]`), so the guard checks
	// what this tsconfig emits. Build it here into a scratch outDir so the
	// assertions run against a fresh emit regardless of the state of `dist` —
	// and fail loudly, never silently skip, if the build itself breaks.
	beforeAll(() => {
		const require = createRequire(import.meta.url);
		const tsc = require.resolve("typescript/bin/tsc");
		const outDir = mkdtempSync(join(tmpdir(), "saas-sdk-surface-"));
		distGenRoot = join(outDir, "generated", "typescript", "src", "gen");
		const result = spawnSync(
			process.execPath,
			[tsc, "-p", join(packageRoot, "tsconfig.json"), "--outDir", outDir],
			{ cwd: packageRoot, encoding: "utf8" },
		);
		if (result.status !== 0) {
			throw new Error(
				`tsc build failed (status ${result.status}):\n${result.stdout ?? ""}\n${result.stderr ?? ""}`,
			);
		}
	}, 120_000);

	it("ships exactly the generated modules reachable from the public API", () => {
		const emitted = [...emittedPbModules()].sort();
		const reachable = [...reachablePbModules()].sort();
		expect(emitted.length).toBeGreaterThan(0);
		// Equality both ways: no unreachable module leaks into `dist` (the original
		// bug — the full message graph shipped), and nothing the API reaches is
		// missing from it.
		expect(emitted).toEqual(reachable);
	});

	it("never ships an internal or adjacent proto surface", () => {
		const emitted = emittedPbModules();
		const leaked = FORBIDDEN_MODULES.filter((module) => emitted.has(module));
		expect(leaked).toEqual([]);
	});
});
