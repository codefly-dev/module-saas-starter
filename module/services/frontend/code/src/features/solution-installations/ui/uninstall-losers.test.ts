import { describe, expect, it } from "vitest";
import type { RoleGrant } from "@/gen/host/installation/v1/installation_pb";
import {
	type AccessView,
	uninstallLosers,
} from "./solution-installations-page";

function view(grants: Partial<RoleGrant>[] | undefined): AccessView {
	return {
		grants: new Map(grants ? [["inst-1", grants as RoleGrant[]]] : []),
		failed: new Set(),
		unreached: () => [],
		roles: () => ["editor", "viewer"],
		name: (grant) => grant.teamId || grant.personId,
	};
}

const row = { installationId: "inst-1" } as Parameters<
	typeof uninstallLosers
>[0];

describe("uninstallLosers", () => {
	// The uninstall confirmation said "Access through this installation is
	// revoked" and named nobody; it names who loses access.
	it("names every holder once, in one phrase", () => {
		expect(
			uninstallLosers(
				row,
				view([
					{ role: "editor", teamId: "expenses-editor", personId: "" },
					{ role: "viewer", teamId: "expenses-viewer", personId: "" },
					{ role: "viewer", teamId: "expenses-editor", personId: "" },
				]),
			),
		).toBe("expenses-editor and expenses-viewer");
	});

	it("says nothing when the holders are unknown or there are none", () => {
		expect(uninstallLosers(row, view(undefined))).toBe("");
		expect(uninstallLosers(row, view([]))).toBe("");
	});
});
