import { describe, expect, it } from "vitest";
import { Installation_Status } from "@/gen/host/installation/v1/installation_pb";
import { teamInstallationRoles } from "./team-installation-access";

describe("teamInstallationRoles", () => {
	const installations = new Map([
		[
			"inst-expenses",
			{ target: "expenses", status: Installation_Status.ACTIVE },
		],
		["inst-people", { target: "people", status: Installation_Status.ACTIVE }],
		["inst-old", { target: "reports", status: Installation_Status.REVOKED }],
	]);
	const grants = new Map([
		[
			"inst-expenses",
			[
				{ role: "user", teamId: "team-b" },
				{ role: "admin", teamId: "team-a" },
			],
		],
		["inst-people", [{ role: "admin", teamId: "team-a" }]],
		["inst-old", [{ role: "admin", teamId: "team-a" }]],
	]);

	// The team page said "This team carries no roles" for a team the
	// solution page listed as its admins: those roles live on installations.
	it("names every role the team holds on a live installation", () => {
		expect(teamInstallationRoles("team-a", installations, grants)).toEqual([
			{ installationId: "inst-expenses", target: "expenses", role: "admin" },
			{ installationId: "inst-people", target: "people", role: "admin" },
		]);
	});

	it("leaves out an uninstalled target and other teams' grants", () => {
		expect(teamInstallationRoles("team-c", installations, grants)).toEqual([]);
		expect(
			teamInstallationRoles("team-b", installations, grants).map((r) => r.role),
		).toEqual(["user"]);
	});
});
