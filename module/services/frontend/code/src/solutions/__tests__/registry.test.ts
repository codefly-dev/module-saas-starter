import { beforeEach, describe, expect, it, vi } from "vitest";

// registry.ts is server-only; the marker package throws when imported outside a
// server component, so neutralize it for the unit test.
vi.mock("server-only", () => ({}));

import {
	findSolution,
	loadSolutions,
	parseManifest,
	registerSolution,
	unregisterSolution,
} from "@/solutions/registry";

function baseManifest(overrides: Record<string, unknown> = {}) {
	return {
		id: "audit",
		nav: { title: "Audit", path: "/s/audit" },
		frontend: {
			type: "module-federation",
			manifestUrl: "https://audit.internal/mf-manifest.json",
			exposedModule: "./Page",
		},
		...overrides,
	};
}

describe("parseManifest", () => {
	it("accepts a well-formed manifest and defaults serviceAlias to id", () => {
		const parsed = parseManifest(baseManifest());
		expect(parsed).not.toBeNull();
		expect(parsed?.backend.serviceAlias).toBe("audit");
		expect(parsed?.frontend.manifestUrl).toBe(
			"https://audit.internal/mf-manifest.json",
		);
	});

	it("preserves an explicit serviceAlias", () => {
		const parsed = parseManifest(
			baseManifest({ backend: { serviceAlias: "audit-svc" } }),
		);
		expect(parsed?.backend.serviceAlias).toBe("audit-svc");
	});

	it("rejects a nav path that is not a safe in-app path", () => {
		for (const path of [
			"https://evil.example/x", // absolute off-site
			"//evil.example", // protocol-relative
			"javascript:alert(1)", // scheme, no leading slash
			"relative/path", // not absolute
			"/x y", // whitespace
			"/x\\y", // backslash
		]) {
			expect(
				parseManifest(baseManifest({ nav: { title: "X", path } })),
				`path ${JSON.stringify(path)} must be rejected`,
			).toBeNull();
		}
	});

	it("rejects a manifest URL that is not an absolute http(s) URL", () => {
		for (const manifestUrl of [
			"/relative/mf-manifest.json",
			"javascript:alert(1)",
			"data:text/javascript,alert(1)",
			"file:///etc/passwd",
			"https://user:pass@audit.internal/mf.json", // embedded credentials
		]) {
			const manifest = baseManifest();
			(manifest.frontend as Record<string, unknown>).manifestUrl = manifestUrl;
			expect(
				parseManifest(manifest),
				`manifestUrl ${JSON.stringify(manifestUrl)} must be rejected`,
			).toBeNull();
		}
	});

	it("rejects structurally invalid payloads", () => {
		expect(parseManifest(null)).toBeNull();
		expect(parseManifest({})).toBeNull();
		expect(parseManifest(baseManifest({ id: "" }))).toBeNull();
		const noExposed = baseManifest();
		(noExposed.frontend as Record<string, unknown>).exposedModule = "";
		expect(parseManifest(noExposed)).toBeNull();
	});
});

describe("registry store", () => {
	beforeEach(() => {
		for (const solution of loadSolutions()) {
			unregisterSolution(solution.id);
		}
	});

	it("stores, orders, finds, and removes registrations", () => {
		const a = parseManifest(
			baseManifest({ id: "a", nav: { title: "A", path: "/s/a", order: 2 } }),
		);
		const b = parseManifest(
			baseManifest({ id: "b", nav: { title: "B", path: "/s/b", order: 1 } }),
		);
		if (!a || !b) throw new Error("fixtures failed to parse");
		registerSolution(a);
		registerSolution(b);

		expect(loadSolutions().map((s) => s.id)).toEqual(["b", "a"]);
		expect(findSolution("a")?.nav.title).toBe("A");

		unregisterSolution("a");
		expect(findSolution("a")).toBeNull();
	});
});
