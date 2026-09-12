import { describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

import {
	checkRuntimeCompatibility,
	satisfiesRange,
} from "@/solutions/compatibility";
import {
	CODEFLY_KIT_VERSION,
	HOST_REACT_VERSION,
} from "@/solutions/host-runtime";
import { parseManifest, type SolutionManifest } from "@/solutions/registry";

function manifest(overrides: Record<string, unknown> = {}): SolutionManifest {
	const frontend = {
		type: "module-federation",
		manifestUrl: "https://example-a.internal/mf-manifest.json",
		exposedModule: "./Page",
		...((overrides.frontend as Record<string, unknown>) ?? {}),
	};
	const parsed = parseManifest({
		id: "example-a",
		nav: { title: "Example A", path: "/s/example-a" },
		frontend,
		...Object.fromEntries(
			Object.entries(overrides).filter(([key]) => key !== "frontend"),
		),
	});
	if (!parsed) {
		throw new Error("fixture failed to parse");
	}
	return parsed;
}

describe("satisfiesRange", () => {
	it.each([
		["19.2.8", "^19.0.0", true],
		["19.2.8", "^19.2.9", false],
		["19.2.8", "~19.2.0", true],
		["19.2.8", "~19.1.0", false],
		["19.2.8", ">=18 <20", true],
		["19.2.8", ">=20", false],
		["19.2.8", "^18.0.0 || ^19.0.0", true],
		["19.2.8", "*", true],
		["19.2.8", "19.2.8", true],
		// Shorthand forms that a hand-written manifest actually uses. Each of these
		// was wrongly refused before: `~19` and `^0` computed the wrong ceiling,
		// and a space after the operator or an x-range tore the comparator apart.
		["19.2.8", "^19", true],
		["19.2.8", "~19", true],
		["0.2.0", "^0", true],
		["0.9.0", "^0", true],
		["1.0.0", "^0", false],
		["19.2.8", ">= 19", true],
		["19.2.8", "19.x", true],
		["19.2.8", "19.2.x", true],
		["19.3.0", "19.2.x", false],
		["19.2.8", "19", true],
		["19.2.8", "19.3", false],
		["0.0.5", "^0.0", true],
		["0.1.0", "^0.0", false],
		["0.2.0", "^0.2.0", true],
		["0.3.0", "^0.2.0", false],
		["0.2.0", "^0.1.0", false],
	])("%s against %s", (version, range, expected) => {
		expect(satisfiesRange(version, range)).toBe(expected);
	});

	it("returns null for a range outside the supported grammar", () => {
		// Null is a refusal, never a pass: a range the host cannot evaluate is not
		// a requirement it can honour.
		for (const range of ["not a range", "", "1.x.y", ">=", "^^1.0.0"]) {
			expect(satisfiesRange("19.2.8", range), range).toBeNull();
		}
	});
});

describe("checkRuntimeCompatibility", () => {
	it("accepts a manifest that declares nothing, as built against today's contract", () => {
		expect(checkRuntimeCompatibility(manifest())).toEqual({
			compatible: true,
			reasons: [],
		});
	});

	it("accepts declared requirements this host satisfies", () => {
		const verdict = checkRuntimeCompatibility(
			manifest({
				schemaVersion: 1,
				frontend: {
					hostContract: 1,
					reactRange: `^${HOST_REACT_VERSION.split(".")[0]}.0.0`,
					shared: {
						"@codefly-dev/ui": `^${CODEFLY_KIT_VERSION}`,
						"@codefly-dev/ui/layout": `^${CODEFLY_KIT_VERSION}`,
						"@codefly-dev/ui/table": `^${CODEFLY_KIT_VERSION}`,
						"@codefly-dev/saas-sdk": "^0.2.0",
					},
				},
			}),
		);
		expect(verdict.compatible).toBe(true);
	});

	it("reports every unmet requirement rather than the first", () => {
		const verdict = checkRuntimeCompatibility(
			manifest({
				schemaVersion: 3,
				frontend: {
					hostContract: 4,
					reactRange: "^17.0.0",
					shared: { "@codefly-dev/saas-ui": "^9.0.0" },
				},
			}),
		);
		expect(verdict.compatible).toBe(false);
		expect(verdict.reasons).toHaveLength(4);
	});

	it("refuses a shared package this host does not publish", () => {
		const verdict = checkRuntimeCompatibility(
			manifest({ frontend: { shared: { "some-other-lib": "^1.0.0" } } }),
		);
		expect(verdict.compatible).toBe(false);
		expect(verdict.reasons[0]).toContain("not published by this host");
	});

	it("checks a shared requirement declared under a __proto__ key", () => {
		// On a plain object literal this key is swallowed and the requirement
		// disappears — accepted and silently ignored, which is the failure this
		// whole gate exists to remove. It must surface as an ordinary refusal.
		// Built with JSON.parse, the way a real request body arrives: that is what
		// makes "__proto__" an OWN property. An object literal would swallow it
		// here in the test and prove nothing.
		const verdict = checkRuntimeCompatibility(
			manifest({
				frontend: { shared: JSON.parse('{"__proto__":"^9.0.0"}') },
			}),
		);
		expect(verdict.compatible).toBe(false);
		expect(verdict.reasons[0]).toContain("not published by this host");
	});

	it("refuses an exposed module key that is not ./Name", () => {
		for (const exposedModule of ["./../secrets", "Page", "./", "./a/b"]) {
			const verdict = checkRuntimeCompatibility(
				manifest({ frontend: { exposedModule } }),
			);
			expect(verdict.compatible, exposedModule).toBe(false);
		}
	});
});
