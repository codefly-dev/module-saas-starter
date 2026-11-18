import { describe, expect, it } from "vitest";
import type { RoleGrant } from "@/gen/host/installation/v1/installation_pb";
import {
	type AccessView,
	installedName,
	uninstallLosers,
	uninstallQuestion,
} from "./access";

const NAMES = new Map([
	["team-1", "expenses-admin"],
	["team-2", "expenses-user"],
]);

function view(grants: Partial<RoleGrant>[] | undefined): AccessView {
	return {
		grants: new Map(grants ? [["inst-1", grants as RoleGrant[]]] : []),
		failed: new Set(),
		unreached: () => [],
		roles: () => ["admin", "user"],
		// As the page does: the team's name when read, else its id.
		name: (grant) => NAMES.get(grant.teamId ?? "") ?? grant.teamId ?? "",
	};
}

const row = {
	installationId: "inst-1",
	solution: { kind: "value" as const, text: "expenses" },
};

describe("uninstallLosers", () => {
	// The uninstall confirmation said "Access through this installation is
	// revoked" and named nobody; it names who loses access.
	it("names every holder once, in one phrase", () => {
		expect(
			uninstallLosers(
				row,
				view([
					{ role: "admin", teamId: "team-1", personId: "" },
					{ role: "user", teamId: "team-2", personId: "" },
					{ role: "user", teamId: "team-1", personId: "" },
				]),
			),
		).toBe("expenses-admin and expenses-user lose");
	});

	it("agrees with a single holder", () => {
		expect(
			uninstallLosers(
				row,
				view([{ role: "admin", teamId: "team-1", personId: "" }]),
			),
		).toBe("expenses-admin loses");
	});

	it("names nobody by an id while a holder's name is not read yet", () => {
		expect(
			uninstallLosers(
				row,
				view([
					{ role: "admin", teamId: "team-1", personId: "" },
					{ role: "user", teamId: "team-unread", personId: "" },
				]),
			),
		).toBe("");
	});

	it("says nothing when the holders are unknown or there are none", () => {
		expect(uninstallLosers(row, view(undefined))).toBe("");
		expect(uninstallLosers(row, view([]))).toBe("");
	});
});

describe("the question asked before an uninstall", () => {
	it("names the act and the solution, who loses access, and what is kept", () => {
		expect(
			uninstallQuestion(
				row,
				view([{ role: "admin", teamId: "team-1", personId: "" }]),
			),
		).toEqual({
			title: "Uninstall expenses?",
			description:
				"expenses-admin loses access to expenses at once. Its record and audit trail remain. Shared supporting module installations are kept.",
		});
	});

	it("says everyone when the holders cannot be named", () => {
		expect(uninstallQuestion(row, view(undefined)).description).toBe(
			"Everyone who holds one of its roles loses access at once. Its record and audit trail remain. Shared supporting module installations are kept.",
		);
	});

	it("names a row the host no longer offers without an identifier", () => {
		const gap = {
			installationId: "inst-1",
			solution: { kind: "gap" as const, label: "Not offered", detail: "" },
		};
		expect(installedName(gap)).toBe("this installation");
		expect(uninstallQuestion(gap, view([])).title).toBe(
			"Uninstall this installation?",
		);
	});
});
