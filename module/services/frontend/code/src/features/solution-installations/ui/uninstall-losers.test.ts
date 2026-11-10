import { describe, expect, it } from "vitest";
import type { RoleGrant } from "@/gen/host/installation/v1/installation_pb";
import {
	type AccessView,
	uninstallLosers,
} from "./solution-installations-page";

const NAMES = new Map([
	["team-1", "expenses-editor"],
	["team-2", "expenses-viewer"],
]);

function view(grants: Partial<RoleGrant>[] | undefined): AccessView {
	return {
		grants: new Map(grants ? [["inst-1", grants as RoleGrant[]]] : []),
		failed: new Set(),
		unreached: () => [],
		roles: () => ["editor", "viewer"],
		// As the page does: the team's name when read, else its id.
		name: (grant) => NAMES.get(grant.teamId ?? "") ?? grant.teamId ?? "",
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
					{ role: "editor", teamId: "team-1", personId: "" },
					{ role: "viewer", teamId: "team-2", personId: "" },
					{ role: "viewer", teamId: "team-1", personId: "" },
				]),
			),
		).toBe("expenses-editor and expenses-viewer lose");
	});

	it("agrees with a single holder", () => {
		expect(
			uninstallLosers(
				row,
				view([{ role: "editor", teamId: "team-1", personId: "" }]),
			),
		).toBe("expenses-editor loses");
	});

	it("names nobody by an id while a holder's name is not read yet", () => {
		expect(
			uninstallLosers(
				row,
				view([
					{ role: "editor", teamId: "team-1", personId: "" },
					{ role: "viewer", teamId: "team-unread", personId: "" },
				]),
			),
		).toBe("");
	});

	it("says nothing when the holders are unknown or there are none", () => {
		expect(uninstallLosers(row, view(undefined))).toBe("");
		expect(uninstallLosers(row, view([]))).toBe("");
	});
});
