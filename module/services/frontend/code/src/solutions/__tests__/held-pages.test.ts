import { describe, expect, it, vi } from "vitest";
import type { ViewerEntitlements } from "@/solutions/entitlements";
import { heldPages } from "@/solutions/projections";

vi.mock("server-only", () => ({}));

// The listing names the pages the host bundles that the viewer holds a role
// on: a module the registry carries without a remote, joined to the viewer's
// entitlements on its target. A module nobody holds (a roleless one, or a page
// of someone else's) is not named.
describe("heldPages", () => {
	const entitlements = (...targets: string[]): ViewerEntitlements => ({
		org: "org",
		viewer: "bob",
		revision: "r",
		byTarget: new Map(
			targets.map((targetId) => [
				targetId,
				{
					targetId,
					healthy: true,
					scopeNodeId: "",
					installationId: `inst-${targetId}`,
				},
			]),
		),
	});
	const modules = new Map([
		["model", "target-model"],
		["store", "target-store"],
		["other-page", "target-other"],
	]);

	it("names each bundled page the viewer is entitled to, by alias", () => {
		expect(heldPages(modules, entitlements("target-model"))).toEqual(["model"]);
	});
	it("names none to a viewer entitled to none", () => {
		expect(heldPages(modules, entitlements())).toEqual([]);
		expect(heldPages(modules, entitlements("target-solution"))).toEqual([]);
	});
});
