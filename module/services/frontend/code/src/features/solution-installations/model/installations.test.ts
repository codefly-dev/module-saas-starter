import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { RoleSchema } from "@/gen/saas/accounts/v1/common_pb";
import {
	AvailableSolutionSchema,
	InstallationHealth,
	InstallationSchema,
	InstallationSummarySchema,
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

const summary = (targetId: string) =>
	create(InstallationSummarySchema, {
		installation: create(InstallationSchema, { id: `i-${targetId}`, targetId }),
		health: InstallationHealth.HEALTHY,
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

	it("joins an installation to the catalogue on its target", () => {
		const [row] = installedRows([summary("t-1")], [target], true);
		expect(row.solution).toEqual({ kind: "value", text: "expenses" });
		expect(row.revision).toMatchObject({
			kind: "value",
			text: "acme/expenses@1.4.0",
		});
	});

	it("never guesses a name for a target the catalogue does not list", () => {
		const [offered] = installedRows([summary("t-gone")], [target], true);
		expect(offered.solution).toMatchObject({
			kind: "gap",
			label: "Not offered",
		});
		const [unread] = installedRows([summary("t-1")], [], false);
		expect(unread.solution).toMatchObject({ kind: "gap", label: "Not loaded" });
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
