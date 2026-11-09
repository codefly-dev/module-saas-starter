import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { RoleGrantSchema } from "@/gen/host/installation/v1/installation_pb";
import {
	InstallationHealth,
	InstallationStatus,
	OrganizationInstallationSchema,
} from "@/gen/saas/accounts/v1/installations_pb";
import { healthFacts, holdersByRole, statusLine } from "./access";
import { installedRows } from "./installations";

const row = (
	status: InstallationStatus,
	extra: {
		upgradeAvailable?: boolean;
		ready?: boolean;
		unmet?: number;
		health?: InstallationHealth;
	} = {},
) =>
	installedRows([
		create(OrganizationInstallationSchema, {
			installation: {
				id: "inst-1",
				status,
				upgradeAvailable: extra.upgradeAvailable ?? false,
			},
			health: extra.health ?? InstallationHealth.HEALTHY,
			readiness:
				extra.ready === undefined
					? undefined
					: {
							ready: extra.ready,
							unmet: Array.from({ length: extra.unmet ?? 0 }, () => ({})),
						},
		}),
	])[0];

const texts = (line: { text: string }[]) => line.map((fact) => fact.text);

describe("an installation's status line", () => {
	it("reads Installed · Ready · Healthy when nothing needs anyone", () => {
		expect(
			texts(statusLine(row(InstallationStatus.ACTIVE, { ready: true }))),
		).toEqual(["Installed", "Ready", "Healthy"]);
	});

	it("names what needs an administrator: an update, conditions, an unreached reach", () => {
		expect(
			texts(
				statusLine(
					row(InstallationStatus.ACTIVE, {
						ready: false,
						unmet: 2,
						upgradeAvailable: true,
					}),
					[
						"role viewer reaches nothing of model.profile:invoke through agent: agent not composed",
					],
				),
			),
		).toEqual([
			"Installed",
			"2 things to fix",
			"Healthy",
			"Update available",
			"Viewers can't reach model through agent",
		]);
	});

	it("says a pending or failed installation is that, and nothing more", () => {
		expect(texts(statusLine(row(InstallationStatus.PENDING)))).toEqual([
			"Being set up",
		]);
		expect(texts(statusLine(row(InstallationStatus.FAILED)))).toEqual([
			"Failed",
		]);
	});

	it("names an unhealthy record, and gives the Setup page only a reported health", () => {
		expect(
			texts(
				statusLine(
					row(InstallationStatus.ACTIVE, {
						health: InstallationHealth.AGENT_DISABLED,
					}),
				),
			),
		).toEqual(["Installed", "Agent disabled"]);
		expect(texts(healthFacts(InstallationHealth.HEALTHY))).toEqual(["Healthy"]);
		expect(healthFacts(InstallationHealth.UNSPECIFIED)).toEqual([]);
	});
});

describe("who holds each role", () => {
	it("lists every declared role with its holders by name, a role nobody holds included", () => {
		const grant = (role: string, teamId: string, personId = "") =>
			create(RoleGrantSchema, { role, teamId, personId });
		const names: Record<string, string> = {
			"team-v": "pages-viewer",
			"team-e": "pages-editor",
			"person-j": "jane@example.com",
		};
		expect(
			holdersByRole(
				["editor", "installer", "viewer"],
				[
					grant("viewer", "team-v"),
					grant("editor", "team-e"),
					grant("editor", "", "person-j"),
				],
				(g) => names[g.teamId || g.personId],
			),
		).toEqual([
			{ role: "editor", holders: ["jane@example.com", "pages-editor"] },
			{ role: "installer", holders: [] },
			{ role: "viewer", holders: ["pages-viewer"] },
		]);
	});
});
