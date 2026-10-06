import { cleanup, fireEvent, screen, within } from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { PlatformCataloguePage } from "./platform-catalogue-page";

afterEach(cleanup);

const notRecorded = (detail: string) => ({
	reason: "CATALOGUE_GAP_REASON_NOT_RECORDED",
	detail,
});
const notObserved = (detail: string) => ({
	reason: "CATALOGUE_GAP_REASON_NOT_OBSERVED",
	detail,
});
const approved = { imageDigest: "sha256:aaa", buildIncarnation: "3" };

// What this host has today for every entry: each state's facts as gaps.
const unknownStates = {
	desired: {
		declaredRevisionGap: notRecorded("No applied presence document."),
		declaredReleaseGap: notRecorded("No applied presence document."),
	},
	authorized: { authorizationGap: notRecorded("No approval record.") },
	applied: { appliedRevisionGap: notRecorded("No applied generation.") },
	observed: {
		observedRevisionGap: notObserved("No cluster state."),
		observationFreshnessGap: notObserved("No cluster state."),
		observedExecutionGap: notObserved("No cluster state."),
		verdictGap: notObserved("No cluster state."),
	},
	withdrawing: {
		withdrawalStateGap: notRecorded("No withdrawal record."),
		credentialRevocationStateGap: notRecorded("No revocation record."),
	},
	retired: { retirementStateGap: notObserved("No retirement controller.") },
	buildSizeGap: notRecorded("Build size is codefly-dev/core#708."),
};

function observedRunning(verdict: string, imageDigest: string) {
	return {
		observedRevisionGap: notObserved("No cluster state."),
		observationFreshnessGap: notObserved("No cluster state."),
		observedExecution: { imageDigest, buildIncarnation: "3" },
		verdict,
	};
}

function solution(name: string, extra: Record<string, unknown>) {
	return {
		kind: "CATALOGUE_ENTRY_KIND_SOLUTION",
		name,
		publisher: `solution:${name}`,
		...unknownStates,
		registration: {
			solutionId: name,
			status: "SOLUTION_REGISTRATION_STATUS_ACTIVE",
		},
		...extra,
	};
}

const catalogue = {
	registryRevision: "12",
	entries: [
		{
			kind: "CATALOGUE_ENTRY_KIND_MODULE",
			name: "billing-module",
			publisherGap: notRecorded(
				"A composed module is known by its prefix alone.",
			),
			...unknownStates,
		},
		solution("drifted-solution", {
			authorized: {
				authorization: { authorizedRevision: "4", approvedExecution: approved },
			},
			observed: observedRunning(
				"CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS",
				"sha256:bbb",
			),
			installations: [
				{
					installation: {
						id: "11111111-1111-1111-1111-111111111111",
						solutionIdentifier: "drifted-solution",
						createdAt: "2026-10-01T00:00:00Z",
					},
					orgName: "Acme",
					agentRelease: {
						publisher: "example",
						name: "drifted",
						version: "1.2.0",
					},
					revisionGap: notRecorded("No installation revision."),
					grantedTeams: [
						{
							grant: { id: "g1", scopePath: "root" },
							subjectLabel: "Finance Team",
							roleName: "Viewer",
						},
					],
					inheritedTeams: [
						{
							grant: { id: "g2", scopePath: "" },
							subjectLabel: "Everyone Team",
							roleName: "Viewer",
						},
					],
				},
			],
		}),
		solution("rogue-solution", {
			authorized: { notAuthorized: { detail: "Approval withdrawn." } },
			observed: observedRunning(
				"CATALOGUE_OBSERVED_VERDICT_RUNNING_UNAUTHORIZED",
				"sha256:aaa",
			),
		}),
		solution("steady-solution", {
			authorized: {
				authorization: { authorizedRevision: "4", approvedExecution: approved },
			},
			observed: observedRunning(
				"CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED",
				"sha256:aaa",
			),
		}),
	],
};

function stateOf(name: string, state: string): HTMLElement {
	const row = screen.getByText(name).closest("tr");
	const cell = row?.querySelector<HTMLElement>(`[aria-label="${state}"]`);
	if (!cell) throw new Error(`no ${state} cell for ${name}`);
	return cell;
}

function serve(body: JsonBodyType) {
	server.use(
		http.post(rpc("PlatformAdminService", "ListPlatformCatalogue"), () =>
			HttpResponse.json(body),
		),
	);
}

describe("PlatformCataloguePage admin container", () => {
	it("shows each entry in the six states, one column each", async () => {
		serve(catalogue);
		renderInApp(<PlatformCataloguePage />);
		await screen.findByText("drifted-solution");
		for (const state of [
			"Desired",
			"Authorized",
			"Applied",
			"Observed",
			"Withdrawing",
			"Retired",
		]) {
			expect(screen.getByRole("columnheader", { name: state })).toBeTruthy();
		}
	});

	it("tells running authorized, differing, running without authorization and unobserved apart", async () => {
		serve(catalogue);
		renderInApp(<PlatformCataloguePage />);
		await screen.findByText("drifted-solution");

		expect(
			within(stateOf("steady-solution", "Observed")).getByText(
				"Running authorized",
			),
		).toBeTruthy();
		expect(
			within(stateOf("drifted-solution", "Observed")).getByText(
				"Differs from authorized",
			),
		).toBeTruthy();
		expect(
			within(stateOf("rogue-solution", "Observed")).getByText(
				"Running without authorization",
			),
		).toBeTruthy();
		expect(
			within(stateOf("rogue-solution", "Authorized")).getByText(
				"Not authorized",
			),
		).toBeTruthy();

		const moduleObserved = stateOf("billing-module", "Observed");
		expect(
			within(moduleObserved).getAllByText("Not observed").length,
		).toBeGreaterThan(0);
		expect(within(moduleObserved).queryByText("Running authorized")).toBeNull();
		expect(
			within(stateOf("billing-module", "Authorized")).getByText("Not recorded"),
		).toBeTruthy();
		expect(
			within(stateOf("billing-module", "Retired")).getByText("Not observed"),
		).toBeTruthy();
	});

	it("says what the host cannot state, and what would supply it", async () => {
		serve(catalogue);
		renderInApp(<PlatformCataloguePage />);
		await screen.findByText("drifted-solution");
		expect(screen.getByText("Not known to this host")).toBeTruthy();
		expect(
			screen.getByText("Build size is codefly-dev/core#708."),
		).toBeTruthy();
		expect(screen.getByText("No approval record.")).toBeTruthy();
		expect(screen.getByText("No withdrawal record.")).toBeTruthy();
	});

	it("opens an entry's installations: organization, agent release, exposed teams", async () => {
		serve(catalogue);
		renderInApp(<PlatformCataloguePage />);
		fireEvent.click(
			await screen.findByRole("button", {
				name: "Show installations of drifted-solution",
			}),
		);
		const installations = screen.getByRole("table", {
			name: "Installations of drifted-solution",
		});
		expect(within(installations).getByText("Acme")).toBeTruthy();
		expect(
			within(installations).getByText("example/drifted@1.2.0"),
		).toBeTruthy();
		// Granted here and inherited from above are listed apart, each named.
		const granted = within(installations)
			.getByText("Granted at this installation")
			.closest("div");
		const inherited = within(installations)
			.getByText("Inherited from above")
			.closest("div");
		if (!granted || !inherited) throw new Error("team groups missing");
		expect(within(granted).getByText("Finance Team")).toBeTruthy();
		expect(within(granted).queryByText("Everyone Team")).toBeNull();
		expect(within(inherited).getByText("Everyone Team")).toBeTruthy();
		expect(within(inherited).queryByText("Finance Team")).toBeNull();
		expect(within(installations).getByText("Not recorded")).toBeTruthy();
	});

	it("says a refused read is refused, not that nothing is deployed", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "ListPlatformCatalogue"), () =>
				HttpResponse.json(
					{
						code: "permission_denied",
						message: "requires platform super_admin role",
					},
					{ status: 403 },
				),
			),
		);
		renderInApp(<PlatformCataloguePage />);
		expect(
			await screen.findByText(/You don't have permission to see the catalogue/),
		).toBeTruthy();
		expect(screen.queryByText(/Nothing is deployed/)).toBeNull();
	});

	it("says an empty platform is empty", async () => {
		serve({ entries: [], registryRevision: "0" });
		renderInApp(<PlatformCataloguePage />);
		expect(await screen.findByText(/Nothing is deployed/)).toBeTruthy();
	});
});
