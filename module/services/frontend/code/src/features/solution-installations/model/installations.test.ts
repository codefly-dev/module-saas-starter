import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { RoleSchema } from "@/gen/saas/accounts/v1/common_pb";
import {
	AvailableSolutionSchema,
	InstallationHealth,
	InstallationSchema,
	OrganizationInstallationSchema,
} from "@/gen/saas/accounts/v1/installations_pb";
import {
	agentIdentifierFor,
	generationText,
	healthView,
	hostRefusal,
	installedRows,
	parseList,
	roleOptionLabel,
} from "./installations";

const target = create(AvailableSolutionSchema, {
	targetId: "t-1",
	routeAlias: "expenses",
	releasePublisher: "acme",
	releaseName: "expenses",
	releaseVersion: "1.4.0",
	openedGeneration: BigInt(2),
	appliedGeneration: BigInt(3),
});

const installed = (targetId: string, withTarget: boolean) =>
	create(OrganizationInstallationSchema, {
		installation: create(InstallationSchema, { id: `i-${targetId}`, targetId }),
		health: InstallationHealth.HEALTHY,
		target: withTarget ? target : undefined,
		rootScopePath: "root_1",
		grantedTeams: [
			{
				grant: {
					id: "g-1",
					subjectId: "team-1",
					roleId: "role-1",
					scopePath: "root_1",
				},
				subjectLabel: "Finance",
				roleName: "Solution user",
			},
		],
		inheritedTeams: [
			{
				grant: {
					id: "g-2",
					subjectId: "team-2",
					roleId: "role-1",
					scopePath: "",
				},
				subjectLabel: "Everyone",
				roleName: "Solution user",
			},
		],
	});

describe("solution installation facts", () => {
	it("offers the applied release as the agent identifier", () => {
		expect(agentIdentifierFor(target)).toBe("acme/expenses:1.4.0");
	});

	it("names both generations when the presence is older than its release", () => {
		expect(generationText(target)).toBe("Generation 3 (opened at 2)");
		expect(
			generationText(
				create(AvailableSolutionSchema, {
					openedGeneration: BigInt(4),
					appliedGeneration: BigInt(4),
				}),
			),
		).toBe("Generation 4");
	});

	it("names an installation by the target the host joined it to", () => {
		const [row] = installedRows([installed("t-1", true)]);
		expect(row.solution).toEqual({ kind: "value", text: "expenses" });
		expect(row.revision).toMatchObject({
			kind: "value",
			text: "acme/expenses@1.4.0",
		});
	});

	it("retains the host's exact setup outcome after a fresh listing", () => {
		for (const message of ["Setup is queued.", "Setup is temporarily unavailable and will retry.", "Setup refused: required configuration is missing.", "Setup acknowledged. Continue the original reviewed installation to finish activation."]) {
			const response = installed("t-1", true);
			response.installation!.setupMessage = message;
			const [row] = installedRows([response]);
			expect(row.failureReason).toBe(message);
		}
	});

	it("never guesses a name for a target the host no longer offers", () => {
		const [row] = installedRows([installed("t-gone", false)]);
		expect(row.solution).toMatchObject({ kind: "gap", label: "Not offered" });
		expect(row.revision).toMatchObject({ kind: "gap", label: "Not offered" });
	});

	it("keeps teams granted here apart from teams inherited from above", () => {
		const [row] = installedRows([installed("t-1", true)]);
		expect(row.grantedTeams).toEqual([
			{
				key: "g-1",
				teamId: "team-1",
				roleId: "role-1",
				team: "Finance",
				role: "Solution user",
				scopePath: "root_1",
			},
		]);
		expect(row.inheritedTeams.map((t) => t.team)).toEqual(["Everyone"]);
	});

	it("reads an unreported health as a gap, not as healthy", () => {
		expect(healthView(InstallationHealth.UNSPECIFIED)).toMatchObject({
			kind: "gap",
		});
		expect(healthView(InstallationHealth.AGENT_REVOKED)).toMatchObject({
			kind: "value",
			tone: "danger",
		});
	});

	it("splits a list one per line or by comma, trimmed and once each", () => {
		expect(parseList(" a \n\nb, a ,c\n")).toEqual(["a", "b", "c"]);
		expect(parseList("   ")).toEqual([]);
	});

	it("labels a role with what it permits", () => {
		const role = create(RoleSchema, {
			name: "Expense reader",
			permissions: [{ resource: "expenses", action: "read" }],
		});
		expect(roleOptionLabel(role)).toBe("Expense reader — expenses:read");
		expect(roleOptionLabel(create(RoleSchema, { name: "Empty" }))).toBe(
			"Empty",
		);
	});

	it("keeps the host's own words for a refusal", () => {
		expect(
			hostRefusal(new ConnectError("no transport", Code.FailedPrecondition)),
		).toBe("no transport");
		expect(hostRefusal(new ConnectError("", Code.Internal))).toBe(
			"The host refused without giving a reason.",
		);
	});
});
