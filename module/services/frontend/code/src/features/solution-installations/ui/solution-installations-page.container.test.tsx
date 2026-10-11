import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

// The page belongs to the active organization from the session. Pin it so the
// container renders the organization's solutions, not the selector prompt.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "org-1",
		switchOrganization: async () => {},
	}),
}));

import { SolutionInstallationsPage } from "./solution-installations-page";

afterEach(cleanup);

const EXPENSES_TARGET = "aaaaaaaa-0000-4000-8000-000000000001";
const LEDGER_TARGET = "aaaaaaaa-0000-4000-8000-000000000002";
const EXPENSES_INSTALLATION = "bbbbbbbb-0000-4000-8000-000000000001";
const READER_ROLE = "cccccccc-0000-4000-8000-000000000001";

const available = {
	solutions: [
		{
			targetId: EXPENSES_TARGET,
			bindingId: "binding-expenses",
			routeAlias: "expenses",
			releasePublisher: "acme",
			releaseName: "expenses",
			releaseVersion: "1.4.0",
			openedGeneration: "2",
			appliedGeneration: "3",
			installed: true,
		},
		{
			targetId: LEDGER_TARGET,
			bindingId: "binding-ledger",
			routeAlias: "ledger",
			releasePublisher: "acme",
			releaseName: "ledger",
			releaseVersion: "0.9.1",
			openedGeneration: "5",
			appliedGeneration: "5",
			installed: false,
		},
	],
};

const installations = {
	installations: [
		{
			installation: {
				id: EXPENSES_INSTALLATION,
				orgId: "org-1",
				targetId: EXPENSES_TARGET,
				rootScopeNodeId: "dddddddd-0000-4000-8000-000000000001",
				status: "INSTALLATION_STATUS_ACTIVE",
				createdAt: "2026-10-01T00:00:00Z",
			},
			health: "INSTALLATION_HEALTH_HEALTHY",
		},
	],
};

const roles = {
	roles: [
		{
			id: READER_ROLE,
			name: "Expense reader",
			orgId: "org-1",
			permissions: [{ resource: "expenses", action: "read" }],
		},
	],
};

function serve({
	availableBody = available,
	installationsBody = installations,
}: {
	availableBody?: JsonBodyType;
	installationsBody?: JsonBodyType;
} = {}) {
	server.use(
		http.post(rpc("InstallationService", "ListAvailableSolutions"), () =>
			HttpResponse.json(availableBody),
		),
		http.post(rpc("InstallationService", "ListInstallations"), () =>
			HttpResponse.json(installationsBody),
		),
		http.post(rpc("PermissionService", "ListRoles"), () =>
			HttpResponse.json(roles),
		),
	);
}

describe("SolutionInstallationsPage admin container", () => {
	it("lists what is available and what is installed, from the host's two reads", async () => {
		serve();
		renderInApp(<SolutionInstallationsPage />);

		const availableTable = await screen.findByRole("table", {
			name: "Available solutions",
		});
		const ledger = within(availableTable).getByText("ledger").closest("tr");
		const expenses = within(availableTable).getByText("expenses").closest("tr");
		if (!ledger || !expenses) throw new Error("catalogue rows missing");
		expect(within(ledger).getByText("acme/ledger@0.9.1")).toBeTruthy();
		expect(within(ledger).getByText("Generation 5")).toBeTruthy();
		expect(within(ledger).getByText("Not installed")).toBeTruthy();
		// Install state is the host's flag; an installed target is not offered twice.
		expect(within(expenses).getByText("Installed")).toBeTruthy();
		expect(
			within(expenses).getByText("Generation 3 (opened at 2)"),
		).toBeTruthy();
		expect(
			(
				within(expenses).getByRole("button", {
					name: "Install expenses",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);

		const installedTable = await screen.findByRole("table", {
			name: "Installed solutions",
		});
		// The installation names only its target; the route and the release come
		// from the catalogue, joined on that target.
		const row = within(installedTable).getByText("expenses").closest("tr");
		if (!row) throw new Error("installed row missing");
		expect(within(row).getByText("acme/expenses@1.4.0")).toBeTruthy();
		expect(within(row).getByText("Generation 3 (opened at 2)")).toBeTruthy();
		expect(within(row).getByText("Healthy")).toBeTruthy();
	});

	it("asks for active installations of this organization only", async () => {
		let asked: unknown;
		serve();
		server.use(
			http.post(
				rpc("InstallationService", "ListInstallations"),
				async ({ request }) => {
					asked = await request.json();
					return HttpResponse.json(installations);
				},
			),
		);
		renderInApp(<SolutionInstallationsPage />);
		await screen.findByRole("table", { name: "Installed solutions" });
		expect(asked).toMatchObject({
			orgId: "org-1",
			status: "INSTALLATION_STATUS_ACTIVE",
		});
	});

	it("installs the chosen target with the role and ceiling the administrator gave", async () => {
		let installed: Record<string, unknown> | undefined;
		serve();
		server.use(
			http.post(
				rpc("InstallationService", "InstallSolution"),
				async ({ request }) => {
					installed = (await request.json()) as Record<string, unknown>;
					return HttpResponse.json({
						id: "bbbbbbbb-0000-4000-8000-000000000002",
						orgId: "org-1",
						targetId: LEDGER_TARGET,
						status: "INSTALLATION_STATUS_ACTIVE",
					});
				},
			),
		);
		renderInApp(<SolutionInstallationsPage />);

		fireEvent.click(
			await screen.findByRole("button", { name: "Install ledger" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			(within(dialog).getByLabelText("Agent identifier") as HTMLInputElement)
				.value,
		).toBe("acme/ledger:0.9.1");

		fireEvent.click(
			within(dialog).getByRole("combobox", { name: "Agent role" }),
		);
		const reader = await screen.findByRole("option", {
			name: "Expense reader — expenses:read",
		});
		fireEvent.pointerDown(reader, { pointerType: "mouse" });
		fireEvent.click(reader);
		fireEvent.change(within(dialog).getByLabelText("Allowed audiences"), {
			target: { value: "solution:binding-ledger\n\nsolution:binding-ledger" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));

		await waitFor(() => expect(installed).toBeDefined());
		expect(installed).toMatchObject({
			orgId: "org-1",
			// The immutable target the row named, never its route alias.
			targetId: LEDGER_TARGET,
			agentIdentifier: "acme/ledger:0.9.1",
			roleId: READER_ROLE,
			allowedAudiences: ["solution:binding-ledger"],
		});
		// Left empty, so the host records the calling administrator as owner.
		expect(installed?.ownerPrincipalId).toBeUndefined();
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("does not install without a role for the solution's agent", async () => {
		let called = false;
		serve();
		server.use(
			http.post(rpc("InstallationService", "InstallSolution"), () => {
				called = true;
				return HttpResponse.json({});
			}),
		);
		renderInApp(<SolutionInstallationsPage />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Install ledger" }),
		);
		const dialog = await screen.findByRole("dialog");
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));
		expect(
			await within(dialog).findByText(
				"Choose the role the solution's agent is granted",
			),
		).toBeTruthy();
		expect(called).toBe(false);
	});

	it("shows the host's refusal of an install in its own words", async () => {
		serve();
		server.use(
			http.post(rpc("InstallationService", "InstallSolution"), () =>
				HttpResponse.json(
					{
						code: "invalid_argument",
						message:
							'audience is not in the host vocabulary: "nowhere" (this host serves ...)',
					},
					{ status: 400 },
				),
			),
		);
		renderInApp(<SolutionInstallationsPage />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Install ledger" }),
		);
		const dialog = await screen.findByRole("dialog");
		fireEvent.click(
			within(dialog).getByRole("combobox", { name: "Agent role" }),
		);
		const reader = await screen.findByRole("option", {
			name: "Expense reader — expenses:read",
		});
		fireEvent.pointerDown(reader, { pointerType: "mouse" });
		fireEvent.click(reader);
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));

		const alert = await within(dialog).findByRole("alert");
		expect(alert.textContent).toContain(
			'audience is not in the host vocabulary: "nowhere"',
		);
		expect(screen.getByRole("dialog")).toBeTruthy();
	});

	it("shows a refused uninstall as the host words it, never as done", async () => {
		let uninstalled: unknown;
		serve();
		server.use(
			http.post(
				rpc("InstallationService", "UninstallSolution"),
				async ({ request }) => {
					uninstalled = await request.json();
					return HttpResponse.json(
						{
							code: "failed_precondition",
							message:
								"cannot uninstall solution: policy log append is not witnessed: no transport is configured",
						},
						{ status: 400 },
					);
				},
			),
		);
		renderInApp(<SolutionInstallationsPage />);

		fireEvent.click(
			await screen.findByRole("button", { name: "Uninstall expenses" }),
		);
		const confirm = await screen.findByRole("alertdialog");
		fireEvent.click(within(confirm).getByRole("button", { name: "Uninstall" }));

		const refusal = await screen.findByText(
			"cannot uninstall solution: policy log append is not witnessed: no transport is configured",
		);
		expect(
			screen.getByText("The host refused to uninstall expenses"),
		).toBeTruthy();
		expect(refusal).toBeTruthy();
		expect(uninstalled).toEqual({
			orgId: "org-1",
			installationId: EXPENSES_INSTALLATION,
		});
		// Still listed: the host did not uninstall it.
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(
			within(
				await screen.findByRole("table", { name: "Installed solutions" }),
			).getByText("expenses"),
		).toBeTruthy();
	});

	it("says why a team cannot be granted here, and grants nothing", async () => {
		let granted = false;
		serve();
		server.use(
			http.post(rpc("PermissionService", "GrantScope"), () => {
				granted = true;
				return HttpResponse.json({});
			}),
			http.post(rpc("PermissionService", "RevokeScope"), () => {
				granted = true;
				return HttpResponse.json({});
			}),
		);
		renderInApp(<SolutionInstallationsPage />);

		const grant = (await screen.findByRole("button", {
			name: "Grant a team access to expenses",
		})) as HTMLButtonElement;
		expect(grant.disabled).toBe(true);
		const reason = document.getElementById(
			grant.getAttribute("aria-describedby") ?? "",
		);
		expect(reason?.textContent).toMatch(/scope path/);
		fireEvent.click(grant);
		expect(granted).toBe(false);
	});

	it("says a refused installation read is refused, and still offers the catalogue", async () => {
		serve({
			installationsBody: undefined,
		});
		server.use(
			http.post(rpc("InstallationService", "ListInstallations"), () =>
				HttpResponse.json(
					{ code: "permission_denied", message: "internal procedure" },
					{ status: 403 },
				),
			),
		);
		renderInApp(<SolutionInstallationsPage />);
		expect(
			await screen.findByText(
				/You don't have permission to see the installed solutions/,
			),
		).toBeTruthy();
		expect(screen.queryByText(/Nothing is installed/)).toBeNull();
		expect(
			await screen.findByRole("table", { name: "Available solutions" }),
		).toBeTruthy();
	});

	it("says an organization with nothing available has nothing available", async () => {
		serve({
			availableBody: { solutions: [] },
			installationsBody: { installations: [] },
		});
		renderInApp(<SolutionInstallationsPage />);
		expect(
			await screen.findByText("No solution is available to this organization."),
		).toBeTruthy();
		expect(
			await screen.findByText("Nothing is installed in this organization."),
		).toBeTruthy();
	});
});
