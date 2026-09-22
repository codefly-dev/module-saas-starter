import { describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// The regression this file exists for: an UNDECLARED major must be read as the
// major that existed when the field was introduced, not as whatever the host
// currently serves. Here the host has moved to contract/schema major 2 while the
// registry's defaults stay frozen at 1, so a manifest that declares nothing is
// correctly refused. If the defaults are ever re-linked to the host constants,
// both assertions below flip to "compatible" and this file fails.
vi.mock("@/solutions/host-runtime", () => ({
	SOLUTION_MANIFEST_SCHEMA_MAJOR: 2,
	SOLUTION_HOST_CONTRACT_MAJOR: 2,
	HOST_REACT_VERSION: "19.2.8",
	HOST_SHARED_VERSIONS: { react: "19.2.8" },
}));

import { checkRuntimeCompatibility } from "@/solutions/compatibility";
import { parseManifest } from "@/solutions/registry";

function undeclaredManifest() {
	const parsed = parseManifest({
		id: "example-a",
		nav: { title: "Example A", path: "/s/example-a" },
		frontend: {
			type: "module-federation",
			manifestUrl: "https://example-a.internal/mf-manifest.json",
			exposedModule: "./Page",
		},
	});
	if (!parsed) {
		throw new Error("fixture failed to parse");
	}
	return parsed;
}

describe("a solution built for an older contract meets an upgraded host", () => {
	it("defaults an undeclared manifest to major 1, not to the host's current major", () => {
		const manifest = undeclaredManifest();
		expect(manifest.schemaVersion).toBe(1);
		expect(manifest.frontend.hostContract).toBe(1);
	});

	it("refuses it, naming both majors", () => {
		const verdict = checkRuntimeCompatibility(undeclaredManifest());
		expect(verdict.compatible).toBe(false);
		expect(verdict.reasons).toHaveLength(2);
		expect(verdict.reasons.join(" ")).toContain("host serves 2");
	});
});
