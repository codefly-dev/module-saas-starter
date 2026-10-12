import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { HOST_SHARED_VERSIONS } from "../host-runtime";
import { CODEFLY_KIT_SHARED } from "../SolutionOutlet";

// The host shares @codefly-dev/ui, @codefly-dev/saas-ui, and @codefly-dev/saas-sdk
// into the Module-Federation scope, each under its own published version. If a
// share entry under-reports its package's real version, a remote bundling the
// newer copy could win singleton resolution and split the instance. The UI kit
// (@codefly-dev/ui + @codefly-dev/saas-ui) is co-versioned; @codefly-dev/saas-sdk
// tracks the API contract and versions independently — so this pins EACH share
// entry's declared version to that package's actual package.json version rather
// than to one shared constant, and a bump to any of them can't drift silently.
//
// What must be shared is DERIVED from each package's own `exports`, never
// restated here. A hand-written list of share keys can only pin the entries
// someone remembered to add to it: a package that starts publishing a new
// subpath — `@codefly-dev/saas-ui/solution`, say — simply would not appear, so
// the gate stayed green while a remote importing the documented path bundled its
// own copy and broke the one-instance invariant the whole share scope exists for.
// Deriving means the next subpath cannot escape the gate by being forgotten.
//
// One package is derived differently, and the reason is a boundary rather than an
// oversight. The host shares the SDK's ROOT only: the SDK's subpaths are the
// surfaces of what composes ON TOP of this host, and the dynamic-dashboard
// channel boundary test (src/features/dashboard/__tests__/boundary.test.ts)
// forbids the mount seam from naming one at all — so the host could not share
// them even if it wanted to, and a consuming module shares its own. Expressed as
// a rule, not a list of names, because this file lives inside the directory that
// boundary test scans.
const KIT_ROOT = join(process.cwd(), "packages");

/** The published kit: the workspace packages under the org's own npm scope. */
function publishedKitPackages(): Array<{
	name: string;
	version: string;
	exports: Record<string, unknown>;
}> {
	return readdirSync(KIT_ROOT)
		.map((directory) => {
			try {
				return JSON.parse(
					readFileSync(join(KIT_ROOT, directory, "package.json"), "utf8"),
				);
			} catch {
				return null;
			}
		})
		.filter(
			(
				manifest,
			): manifest is {
				name: string;
				version: string;
				exports: Record<string, unknown>;
			} => !!manifest?.name?.startsWith("@codefly-dev/"),
		);
}

/**
 * The Module-Federation share key each published entry point is imported by:
 * the bare package name for ".", and name + subpath for "./layout". A stylesheet
 * is not a module and cannot be shared, so only JS entry points count.
 */
function shareKeysOf(manifest: {
	name: string;
	exports: Record<string, unknown>;
}): string[] {
	return Object.entries(manifest.exports ?? { ".": "" })
		.filter(([subpath, target]) => {
			const file =
				typeof target === "string"
					? target
					: ((target as Record<string, string>)?.import ??
						(target as Record<string, string>)?.default ??
						"");
			return !subpath.endsWith(".css") && !file.endsWith(".css");
		})
		.map(([subpath]) =>
			subpath === "." ? manifest.name : `${manifest.name}${subpath.slice(1)}`,
		);
}

/** The one package whose subpaths the host may not name (see above). */
const ROOT_ONLY = "@codefly-dev/saas-sdk";

const published = publishedKitPackages();
const expectedShares = published.flatMap((manifest) =>
	(manifest.name === ROOT_ONLY ? [manifest.name] : shareKeysOf(manifest)).map(
		(key) => ({ key, version: manifest.version }),
	),
);

describe("shared kit versions match their published packages", () => {
	it("shares the same plugin runtime through direct and UI entry points", () => {
		const direct =
			CODEFLY_KIT_SHARED["@codefly-dev/saas-plugin-react/runtime"].lib();
		const wrapper =
			CODEFLY_KIT_SHARED["@codefly-dev/ui/plugin-host/runtime"].lib();
		expect(direct.PluginRuntimeProvider).toBe(wrapper.PluginRuntimeProvider);
		expect(direct.usePluginRuntime).toBe(wrapper.usePluginRuntime);
	});
	it("finds the published kit packages to derive from", () => {
		expect(published.map(({ name }) => name).sort()).toEqual([
			"@codefly-dev/saas-plugin-contract",
			"@codefly-dev/saas-plugin-react",
			"@codefly-dev/saas-sdk",
			"@codefly-dev/saas-ui",
			"@codefly-dev/ui",
		]);
	});

	it("keeps the two UI kits co-versioned", () => {
		const version = (name: string) =>
			published.find((manifest) => manifest.name === name)?.version;
		expect(version("@codefly-dev/saas-ui")).toBe(version("@codefly-dev/ui"));
	});

	it.each(expectedShares.map(({ key, version }) => [key, version]))(
		"%s shares its real package.json version",
		(shareKey, version) => {
			const entry =
				CODEFLY_KIT_SHARED[shareKey as keyof typeof CODEFLY_KIT_SHARED];
			expect(entry, `missing share entry for ${shareKey}`).toBeDefined();
			expect(entry.version).toBe(version);
			expect(HOST_SHARED_VERSIONS[shareKey]).toBe(entry.version);
		},
	);

	// The other direction: a share key for an entry point no package publishes is
	// dead weight a remote can never resolve against, and usually a typo in a path
	// the tests above would never look up. It also catches the reverse of the
	// boundary above — a subpath of the root-only package finding its way in.
	it("shares no entry point the kit does not publish", () => {
		expect(Object.keys(CODEFLY_KIT_SHARED).sort()).toEqual(
			expectedShares.map(({ key }) => key).sort(),
		);
	});
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
