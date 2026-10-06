import { cleanup, fireEvent, screen, within } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { PlatformCataloguePage } from "./platform-catalogue-page";

afterEach(cleanup);

const notRecorded = (detail: string) => ({
	reason: "CATALOGUE_GAP_REASON_NOT_RECORDED",
	detail,
});
const presenceGaps = {
	declaredReleaseGap: notRecorded("No applied presence document."),
	buildDigestGap: notRecorded("No applied presence document."),
	generationGap: notRecorded("No applied presence document."),
	buildSizeGap: notRecorded("Build size is codefly-dev/core#708."),
};
const declared = { imageDigest: "sha256:aaa", buildIncarnation: "3" };

const catalogue = {
	registryRevision: "12",
	entries: [
		{
			kind: "CATALOGUE_ENTRY_KIND_MODULE",
			name: "billing-module",
			publisherGap: notRecorded(
				"A composed module is known by its prefix alone.",
			),
			...presenceGaps,
			runningGap: {
				reason: "CATALOGUE_GAP_REASON_NOT_OBSERVED",
				detail: "No cluster state.",
			},
		},
		{
			kind: "CATALOGUE_ENTRY_KIND_SOLUTION",
			name: "drifted-solution",
			publisher: "solution:drifted-solution",
			...presenceGaps,
			running: {
				declared,
				observed: { imageDigest: "sha256:bbb", buildIncarnation: "3" },
				verdict: "CATALOGUE_RUNNING_VERDICT_DIFFERS",
			},
			registration: {
				solutionId: "drifted-solution",
				status: "SOLUTION_REGISTRATION_STATUS_ACTIVE",
			},
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
					revisionGap: notRecorded("Not pinned to a presence revision."),
					exposedTeams: [
						{
							grant: { id: "g1", scopePath: "root" },
							subjectLabel: "Finance Team",
							roleName: "Viewer",
						},
					],
				},
			],
		},
		{
			kind: "CATALOGUE_ENTRY_KIND_SOLUTION",
			name: "steady-solution",
			publisher: "solution:steady-solution",
			...presenceGaps,
			running: {
				declared,
				observed: declared,
				verdict: "CATALOGUE_RUNNING_VERDICT_MATCHES",
			},
			registration: {
				solutionId: "steady-solution",
				status: "SOLUTION_REGISTRATION_STATUS_ACTIVE",
			},
		},
	],
};

function rowOf(name: string): HTMLElement {
	const row = screen.getByText(name).closest("tr");
	if (!row) throw new Error(`no row for ${name}`);
	return row;
}

describe("PlatformCataloguePage admin container", () => {
	it("shows a match, a mismatch and an unobserved entry for what they are", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "ListPlatformCatalogue"), () =>
				HttpResponse.json(catalogue),
			),
		);
		renderInApp(<PlatformCataloguePage />);
		await screen.findByText("drifted-solution");

		expect(
			within(rowOf("drifted-solution")).getByText("Differs from declared"),
		).toBeTruthy();
		expect(
			within(rowOf("steady-solution")).getByText("Matches declared"),
		).toBeTruthy();
		const moduleRow = rowOf("billing-module");
		expect(within(moduleRow).getByText("Not observed")).toBeTruthy();
		expect(within(moduleRow).queryByText("Matches declared")).toBeNull();
		expect(within(moduleRow).getByText("Module")).toBeTruthy();

		// Build size is unavailable on every row until the presence document
		// carries it, and the page says what would supply it.
		for (const name of [
			"billing-module",
			"drifted-solution",
			"steady-solution",
		]) {
			const sizeCell = within(rowOf(name))
				.getAllByText("Not recorded")
				.find((cell) => cell.getAttribute("title")?.includes("core#708"));
			expect(sizeCell).toBeTruthy();
		}
		expect(screen.getByText("Not known to this host")).toBeTruthy();
		expect(
			screen.getByText("Build size is codefly-dev/core#708."),
		).toBeTruthy();
	});

	it("opens an entry's installations: organization, agent release, exposed teams", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "ListPlatformCatalogue"), () =>
				HttpResponse.json(catalogue),
			),
		);
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
		expect(within(installations).getByText("Finance Team")).toBeTruthy();
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
		server.use(
			http.post(rpc("PlatformAdminService", "ListPlatformCatalogue"), () =>
				HttpResponse.json({ entries: [], registryRevision: "0" }),
			),
		);
		renderInApp(<PlatformCataloguePage />);
		expect(await screen.findByText(/Nothing is deployed/)).toBeTruthy();
	});
});
