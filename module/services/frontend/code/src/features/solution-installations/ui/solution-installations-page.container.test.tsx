import { QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Page from "@/app/admin/solutions/page";
import { FRONTEND_NAVIGATION } from "@/gen/saas/frontend/v1/plugin_catalog";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { canPresent } from "@/lib/plugins/presentation";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

const session = vi.hoisted(() => ({
	organizationId: "org-1",
	isAuthenticated: true,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: async () => {},
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => session }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const checkedRequests: { actual: unknown; expected: unknown }[] = [];
async function assertBody(request: Request, expected: unknown) {
	const actual = await request.json();
	checkedRequests.push({ actual, expected });
	expect(actual).toEqual(expected);
}

beforeEach(() => {
	session.organizationId = "org-1";
	session.isAuthenticated = true;
	session.orgRole = "owner";
	session.platformRole = undefined;
	checkedRequests.length = 0;
	vi.clearAllMocks();
});
afterEach(() => {
	cleanup();
	// MSW turns handler exceptions into HTTP errors. Assert again here so a
	// malformed request can never pass a test that expects a host refusal.
	for (const { actual, expected } of checkedRequests)
		expect(actual).toEqual(expected);
});

const EXPENSES_TARGET = "aaaaaaaa-0000-4000-8000-000000000001";
const LEDGER_TARGET = "aaaaaaaa-0000-4000-8000-000000000002";
// The consent the page sends: the reviewed target and generation, a zero
// (omitted) revision for a first install, and a fresh operation id.
const LEDGER_APPROVAL = {
	targetId: LEDGER_TARGET,
	generation: "5",
	operationId: expect.any(String),
};
const EXPENSES_INSTALLATION = "bbbbbbbb-0000-4000-8000-000000000001";
const READER_ROLE = "cccccccc-0000-4000-8000-000000000001";
const USE_ROLE = "cccccccc-0000-4000-8000-000000000002";
const FINANCE_TEAM = "eeeeeeee-0000-4000-8000-000000000001";
const SUPPORT_TEAM = "eeeeeeee-0000-4000-8000-000000000002";

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
			// The host joins the installation to its target's catalogue row.
			target: available.solutions[0],
			rootScopePath: "dddddddd_0000_4000_8000_000000000001",
			grantedTeams: [
				{
					grant: {
						id: "grant-1",
						subjectId: FINANCE_TEAM,
						subjectKind: "SUBJECT_KIND_TEAM",
						roleId: USE_ROLE,
						scopePath: "dddddddd_0000_4000_8000_000000000001",
					},
					subjectLabel: "Finance",
					roleName: "Solution user",
				},
			],
			inheritedTeams: [
				{
					grant: {
						id: "grant-2",
						subjectId: "eeeeeeee-0000-4000-8000-000000000009",
						subjectKind: "SUBJECT_KIND_TEAM",
						roleId: USE_ROLE,
						scopePath: "",
					},
					subjectLabel: "Everyone",
					roleName: "Solution user",
				},
			],
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
		{
			id: USE_ROLE,
			name: "Solution user",
			orgId: "org-1",
			permissions: [{ resource: "solution", action: "use" }],
		},
	],
};

const teams = {
	teams: [
		{ id: FINANCE_TEAM, orgId: "org-1", name: "Finance" },
		{ id: SUPPORT_TEAM, orgId: "org-1", name: "Support" },
	],
};

// Picks an option in a kit Select, the way the other admin tests do.
async function choose(dialog: HTMLElement, label: string, option: string) {
	const control = within(dialog).getByRole("combobox", { name: label });
	await waitFor(() =>
		expect((control as HTMLButtonElement).disabled).toBe(false),
	);
	fireEvent.click(control);
	const item = await screen.findByRole("option", { name: option });
	fireEvent.pointerDown(item, { pointerType: "mouse" });
	fireEvent.click(item);
}

function serve({
	availableBody = available,
	installationsBody = installations,
	orgId = "org-1",
	rolesBody = roles,
	teamsBody = teams,
}: {
	availableBody?: JsonBodyType;
	installationsBody?: JsonBodyType;
	orgId?: string;
	rolesBody?: JsonBodyType;
	teamsBody?: JsonBodyType;
} = {}) {
	const reads = { available: 0, installed: 0, roles: 0, teams: 0 };
	server.use(
		http.post(
			rpc("InstallationService", "ListAvailableSolutions"),
			async ({ request }) => {
				await assertBody(request, { orgId, pageSize: 500 });
				reads.available++;
				return HttpResponse.json(availableBody);
			},
		),
		http.post(
			rpc("InstallationService", "ListOrganizationInstallations"),
			async ({ request }) => {
				await assertBody(request, { orgId, pageSize: 500 });
				reads.installed++;
				return HttpResponse.json(installationsBody);
			},
		),
		http.post(rpc("PermissionService", "ListRoles"), async ({ request }) => {
			await assertBody(request, { orgId });
			reads.roles++;
			return HttpResponse.json(rolesBody);
		}),
		http.post(rpc("TeamService", "ListTeams"), async ({ request }) => {
			await assertBody(request, { orgId });
			reads.teams++;
			return HttpResponse.json(teamsBody);
		}),
	);
	return reads;
}

async function openInstall() {
	fireEvent.click(
		await screen.findByRole("button", { name: "Install ledger" }),
	);
	const dialog = await screen.findByRole("dialog");
	await choose(dialog, "Agent role", "Expense reader — expenses:read");
	return dialog;
}

async function openGrant() {
	fireEvent.click(
		await screen.findByRole("button", {
			name: "Grant a team access to expenses",
		}),
	);
	const dialog = await screen.findByRole("dialog");
	await choose(dialog, "Team", "Support");
	await choose(dialog, "Role", "Solution user — solution:use");
	return dialog;
}

function expectFieldError(control: HTMLElement, message: string) {
	expect(control.getAttribute("aria-invalid")).toBe("true");
	const descriptions = (control.getAttribute("aria-describedby") ?? "")
		.split(" ")
		.map((id) => document.getElementById(id));
	const error = descriptions.find(
		(element) => element?.textContent === message,
	);
	expect(error?.getAttribute("role")).toBe("alert");
}

function refusal(message: string, code = "permission_denied") {
	return HttpResponse.json(
		{ code, message },
		{ status: code === "permission_denied" ? 403 : 400 },
	);
}

describe("SolutionInstallationsPage admin container", () => {
	it("lists what is available and what is installed, from the host's two reads", async () => {
		serve();
		renderInApp(<Page />);

		const availableTable = await screen.findByRole("table", {
			name: "Available modules and solutions",
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
			name: "Installed modules and solutions",
		});
		// The route and the release are the catalogue row the host joined to the
		// installation's target.
		const row = within(installedTable).getByText("expenses").closest("tr");
		if (!row) throw new Error("installed row missing");
		expect(within(row).getByText("acme/expenses@1.4.0")).toBeTruthy();
		expect(within(row).getByText("Generation 3 (opened at 2)")).toBeTruthy();
		expect(within(row).getByText("Healthy")).toBeTruthy();
		// Granted here and inherited from above are kept apart; only the first is
		// removable here.
		const granted = within(row).getByRole("list", {
			name: "Teams granted expenses",
		});
		expect(within(granted).getByText("Finance")).toBeTruthy();
		expect(within(granted).queryByText("Everyone")).toBeNull();
		expect(
			within(row).getByText(/Inherited from above: Everyone/),
		).toBeTruthy();
		expect(
			within(row).queryByRole("button", {
				name: "Remove Everyone from expenses",
			}),
		).toBeNull();
	});

	it("asks for this organization's installations through the org-admin read", async () => {
		let asked: unknown;
		serve();
		server.use(
			http.post(
				rpc("InstallationService", "ListOrganizationInstallations"),
				async ({ request }) => {
					asked = await request.json();
					return HttpResponse.json(installations);
				},
			),
		);
		renderInApp(<Page />);
		await screen.findByRole("table", {
			name: "Installed modules and solutions",
		});
		expect(asked).toEqual({ orgId: "org-1", pageSize: 500 });
	});

	it("installs the chosen target with the role and ceiling the administrator gave", async () => {
		let installed: Record<string, unknown> | undefined;
		const availableBody = structuredClone(available);
		const installationsBody = structuredClone(installations);
		const reads = serve({ availableBody, installationsBody });
		server.use(
			http.post(
				rpc("InstallationService", "InstallSolution"),
				async ({ request }) => {
					installed = (await request.json()) as Record<string, unknown>;
					availableBody.solutions[1].installed = true;
					installationsBody.installations.push({
						...structuredClone(installations.installations[0]),
						installation: {
							...installations.installations[0].installation,
							id: "bbbbbbbb-0000-4000-8000-000000000002",
							targetId: LEDGER_TARGET,
						},
						target: availableBody.solutions[1],
						grantedTeams: [],
						inheritedTeams: [],
					});
					return HttpResponse.json({
						id: "bbbbbbbb-0000-4000-8000-000000000002",
						orgId: "org-1",
						targetId: LEDGER_TARGET,
						status: "INSTALLATION_STATUS_ACTIVE",
					});
				},
			),
		);
		renderInApp(<Page />);

		fireEvent.click(
			await screen.findByRole("button", { name: "Install ledger" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			(within(dialog).getByLabelText("Agent identifier") as HTMLInputElement)
				.value,
		).toBe("acme/ledger:0.9.1");

		await choose(dialog, "Agent role", "Expense reader — expenses:read");
		fireEvent.change(within(dialog).getByLabelText("Allowed audiences"), {
			target: { value: "solution:binding-ledger\n\nsolution:binding-ledger" },
		});
		fireEvent.change(within(dialog).getByLabelText("Agent name (optional)"), {
			target: { value: "  Ledger agent  " },
		});
		fireEvent.change(
			within(dialog).getByLabelText("Access boundary name (optional)"),
			{ target: { value: "  Ledger boundary  " } },
		);
		fireEvent.change(within(dialog).getByLabelText("Allowed resource kinds"), {
			target: { value: "entry, entry\naccount" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));

		await waitFor(() => expect(installed).toBeDefined());
		expect(installed).toEqual({
			orgId: "org-1",
			// The immutable target the row named, never its route alias.
			targetId: LEDGER_TARGET,
			approval: LEDGER_APPROVAL,
			agentIdentifier: "acme/ledger:0.9.1",
			roleId: READER_ROLE,
			allowedAudiences: ["solution:binding-ledger"],
			displayName: "Ledger agent",
			rootScopeLabel: "Ledger boundary",
			allowedScopes: ["entry", "account"],
		});
		// Left empty, so the host records the calling administrator as owner.
		expect(installed?.ownerPrincipalId).toBeUndefined();
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(reads.available).toBe(2);
		expect(reads.installed).toBe(2);
		expect(
			(
				screen.getByRole("button", {
					name: "Install ledger",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		expect(
			within(
				screen.getByRole("table", { name: "Installed modules and solutions" }),
			).getByText("ledger"),
		).toBeTruthy();
		expect(toast.success).toHaveBeenCalledWith("ledger installation pending");
	});

	it("reinstalls after revoke using the revision reported by the installation listing", async () => {
		serve();
		let sent: Record<string, unknown> | undefined;
		server.use(
			http.post(
				rpc("InstallationService", "ListOrganizationInstallations"),
				() =>
					HttpResponse.json({
						installations: [
							{
								installation: {
									id: "revoked-ledger",
									orgId: "org-1",
									targetId: LEDGER_TARGET,
									status: "INSTALLATION_STATUS_REVOKED",
									revision: "17",
								},
							},
						],
					}),
			),
			http.post(
				rpc("InstallationService", "InstallSolution"),
				async ({ request }) => {
					sent = (await request.json()) as Record<string, unknown>;
					return HttpResponse.json({});
				},
			),
		);
		renderInApp(<Page />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Install ledger" }),
		);
		const dialog = await screen.findByRole("dialog");
		await choose(dialog, "Agent role", "Expense reader — expenses:read");
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));
		await waitFor(() => expect(sent).toBeDefined());
		expect(sent?.approval).toEqual({
			...LEDGER_APPROVAL,
			expectedRevision: "17",
		});
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
		renderInApp(<Page />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Install ledger" }),
		);
		const dialog = await screen.findByRole("dialog");
		await waitFor(() =>
			expect(
				(
					within(dialog).getByRole("button", {
						name: "Install",
					}) as HTMLButtonElement
				).disabled,
			).toBe(false),
		);
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
		renderInApp(<Page />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Install ledger" }),
		);
		const dialog = await screen.findByRole("dialog");
		await choose(dialog, "Agent role", "Expense reader — expenses:read");
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));

		const alert = await within(dialog).findByRole("alert");
		expect(alert.textContent).toContain(
			'audience is not in the host vocabulary: "nowhere"',
		);
		expect(screen.getByRole("dialog")).toBeTruthy();
		expect(toast.success).not.toHaveBeenCalled();
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
		renderInApp(<Page />);

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
		expect(toast.success).not.toHaveBeenCalled();
		expect(uninstalled).toEqual({
			orgId: "org-1",
			installationId: EXPENSES_INSTALLATION,
		});
		// Still listed: the host did not uninstall it.
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(
			within(
				await screen.findByRole("table", {
					name: "Installed modules and solutions",
				}),
			).getByText("expenses"),
		).toBeTruthy();
	});

	it("grants a team by installation, team and role, never by scope path", async () => {
		let granted: Record<string, unknown> | undefined;
		const availableBody = structuredClone(available);
		const installationsBody = structuredClone(installations);
		// A grant does not make a disabled installation healthy.
		installationsBody.installations[0].health =
			"INSTALLATION_HEALTH_AGENT_DISABLED";
		const reads = serve({ availableBody, installationsBody });
		server.use(
			http.post(
				rpc("InstallationService", "GrantInstallationToTeam"),
				async ({ request }) => {
					granted = (await request.json()) as Record<string, unknown>;
					installationsBody.installations[0].grantedTeams.push({
						grant: {
							...installations.installations[0].grantedTeams[0].grant,
							id: "grant-3",
							subjectId: SUPPORT_TEAM,
						},
						subjectLabel: "Support",
						roleName: "Solution user",
					});
					// The host's available release advances during the operation.
					availableBody.solutions[0].appliedGeneration = "4";
					return HttpResponse.json({ grant: { id: "grant-3" } });
				},
			),
		);
		renderInApp(<Page />);

		fireEvent.click(
			await screen.findByRole("button", {
				name: "Grant a team access to expenses",
			}),
		);
		const dialog = await screen.findByRole("dialog");
		await choose(dialog, "Team", "Support");
		await choose(dialog, "Role", "Solution user — solution:use");
		fireEvent.click(within(dialog).getByRole("button", { name: "Grant" }));

		await waitFor(() => expect(granted).toBeDefined());
		expect(granted).toEqual({
			orgId: "org-1",
			installationId: EXPENSES_INSTALLATION,
			teamId: SUPPORT_TEAM,
			roleId: USE_ROLE,
		});
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(reads.available).toBe(2);
		expect(reads.installed).toBe(2);
		expect(
			within(
				screen.getByRole("table", { name: "Available modules and solutions" }),
			).getByText("Generation 4 (opened at 2)"),
		).toBeTruthy();
		expect(
			within(
				screen.getByRole("list", { name: "Teams granted expenses" }),
			).getByText("Support"),
		).toBeTruthy();
		expect(
			within(
				screen.getByRole("table", { name: "Installed modules and solutions" }),
			).getByText("Agent disabled"),
		).toBeTruthy();
		expect(toast.success).toHaveBeenCalledExactlyOnceWith(
			"Team grant recorded for expenses",
		);
	});

	it("shows the host's refusal of a grant in its own words", async () => {
		serve();
		server.use(
			http.post(rpc("InstallationService", "GrantInstallationToTeam"), () =>
				HttpResponse.json(
					{
						code: "invalid_argument",
						message:
							"role does not permit using a solution: role r does not permit solution:use, so a grant of it would expose nothing",
					},
					{ status: 400 },
				),
			),
		);
		renderInApp(<Page />);
		fireEvent.click(
			await screen.findByRole("button", {
				name: "Grant a team access to expenses",
			}),
		);
		const dialog = await screen.findByRole("dialog");
		await choose(dialog, "Team", "Support");
		await choose(dialog, "Role", "Expense reader — expenses:read");
		fireEvent.click(within(dialog).getByRole("button", { name: "Grant" }));
		const alert = await within(dialog).findByRole("alert");
		expect(alert.textContent).toContain(
			"so a grant of it would expose nothing",
		);
		expect(toast.success).not.toHaveBeenCalled();
	});

	it("removes a team through RevokeInstallationFromTeam and shows a refusal as the host words it", async () => {
		let revoked: unknown;
		serve();
		server.use(
			http.post(
				rpc("InstallationService", "RevokeInstallationFromTeam"),
				async ({ request }) => {
					revoked = await request.json();
					return HttpResponse.json(
						{
							code: "failed_precondition",
							message:
								"cannot revoke installation from team: policy log unreachable: this host has no policy log, so authority cannot be narrowed",
						},
						{ status: 400 },
					);
				},
			),
		);
		renderInApp(<Page />);

		fireEvent.click(
			await screen.findByRole("button", {
				name: "Remove Finance from expenses",
			}),
		);

		expect(
			await screen.findByText(
				"The host refused to remove Finance from expenses",
			),
		).toBeTruthy();
		expect(
			screen.getByText(
				"cannot revoke installation from team: policy log unreachable: this host has no policy log, so authority cannot be narrowed",
			),
		).toBeTruthy();
		expect(toast.success).not.toHaveBeenCalled();
		expect(revoked).toEqual({
			orgId: "org-1",
			installationId: EXPENSES_INSTALLATION,
			teamId: FINANCE_TEAM,
			roleId: USE_ROLE,
		});
		// Still granted: the host did not remove it.
		expect(
			within(
				screen.getByRole("list", { name: "Teams granted expenses" }),
			).getByText("Finance"),
		).toBeTruthy();
	});

	it("says a refused installation read is refused, and still offers the catalogue", async () => {
		serve({
			installationsBody: undefined,
		});
		server.use(
			http.post(
				rpc("InstallationService", "ListOrganizationInstallations"),
				() =>
					HttpResponse.json(
						{
							code: "permission_denied",
							message: "requires organization admin",
						},
						{ status: 403 },
					),
			),
		);
		renderInApp(<Page />);
		expect(
			await screen.findByText(
				/You don't have permission to see the installed modules and solutions/,
			),
		).toBeTruthy();
		expect(screen.getByRole("alert").textContent).toContain(
			"requires organization admin",
		);
		expect(toast.success).not.toHaveBeenCalled();
		expect(screen.queryByText(/Nothing is installed/)).toBeNull();
		expect(
			await screen.findByRole("table", {
				name: "Available modules and solutions",
			}),
		).toBeTruthy();
	});

	it("says an organization with nothing available has nothing available", async () => {
		serve({
			availableBody: { solutions: [] },
			installationsBody: { installations: [] },
		});
		renderInApp(<Page />);
		expect(
			await screen.findByText(
				"No module or solution is available to this organization. Ask your platform operator to make one available, then refresh this list.",
			),
		).toBeTruthy();
		expect(
			await screen.findByText(
				"Nothing is installed in this organization. Once a module or solution is available above, choose it to install it.",
			),
		).toBeTruthy();
	});
});

describe("organization administration boundary", () => {
	it.each([
		{ orgRole: "member", platformRole: undefined, allowed: false },
		{ orgRole: "member", platformRole: "support", allowed: false },
		{ orgRole: "member", platformRole: "billing", allowed: false },
		{ orgRole: "admin", platformRole: undefined, allowed: true },
		{ orgRole: "owner", platformRole: undefined, allowed: true },
		{ orgRole: "member", platformRole: "super_admin", allowed: true },
	] as const)(
		"uses the same navigation and route capability for $orgRole / $platformRole",
		async ({ orgRole, platformRole, allowed }) => {
			session.orgRole = orgRole;
			session.platformRole = platformRole;
			const reads = serve();
			const navigation = FRONTEND_NAVIGATION.find(
				(item) => item.id === "admin-solutions",
			);
			expect(navigation?.requiredPermission).toBe("roles:write");
			if (!navigation) throw new Error("Solutions navigation missing");
			expect(canPresent(navigation, session)).toBe(allowed);
			const { client } = renderInApp(<Page />);
			if (allowed) {
				await screen.findByRole("table", {
					name: "Installed modules and solutions",
				});
				expect(reads.available).toBe(1);
				expect(reads.installed).toBe(1);
			} else {
				expect(
					screen.getByText("Organization administrator required"),
				).toBeTruthy();
				expect(client.getQueryCache().getAll()).toHaveLength(0);
				expect(Object.values(reads)).toEqual([0, 0, 0, 0]);
			}
		},
	);

	it("does not read without an active organization", () => {
		session.organizationId = "";
		const reads = serve();
		renderInApp(<Page />);
		expect(
			screen.getByText(
				"Select an organization to manage its modules and solutions",
			),
		).toBeTruthy();
		expect(Object.values(reads)).toEqual([0, 0, 0, 0]);
	});

	it("reads every page with the organization and opaque token for each listing", async () => {
		const availableTokens: unknown[] = [];
		const installedTokens: unknown[] = [];
		serve();
		server.use(
			http.post(
				rpc("InstallationService", "ListAvailableSolutions"),
				async ({ request }) => {
					const second = availableTokens.length > 0;
					await assertBody(request, {
						orgId: "org-1",
						pageSize: 500,
						...(second ? { pageToken: "available-next" } : {}),
					});
					availableTokens.push(second);
					return HttpResponse.json({
						solutions: [available.solutions[second ? 1 : 0]],
						nextPageToken: second ? "" : "available-next",
					});
				},
			),
			http.post(
				rpc("InstallationService", "ListOrganizationInstallations"),
				async ({ request }) => {
					const second = installedTokens.length > 0;
					await assertBody(request, {
						orgId: "org-1",
						pageSize: 500,
						...(second ? { pageToken: "installed-next" } : {}),
					});
					installedTokens.push(second);
					return HttpResponse.json({
						installations: second
							? [
									{
										...installations.installations[0],
										installation: {
											...installations.installations[0].installation,
											id: "installation-2",
										},
										target: available.solutions[1],
									},
								]
							: installations.installations,
						nextPageToken: second ? "" : "installed-next",
					});
				},
			),
		);
		renderInApp(<Page />);
		for (const name of [
			"Available modules and solutions",
			"Installed modules and solutions",
		]) {
			const table = await screen.findByRole("table", { name });
			expect(within(table).getByText("expenses")).toBeTruthy();
			expect(within(table).getByText("ledger")).toBeTruthy();
		}
		expect(availableTokens).toEqual([false, true]);
		expect(installedTokens).toEqual([false, true]);
	});

	it.each(["install", "grant"])(
		"discards the %s draft and scopes all reads and the next grant after an organization switch",
		async (draft) => {
			serve();
			const { client, rerender } = renderInApp(<Page />);
			if (draft === "install") await openInstall();
			else await openGrant();

			const secondAvailable = structuredClone(available);
			secondAvailable.solutions[0].routeAlias = "payables";
			secondAvailable.solutions[1].routeAlias = "receipts";
			const secondInstalled = structuredClone(installations);
			secondInstalled.installations[0].installation.id = "installation-org-2";
			secondInstalled.installations[0].installation.orgId = "org-2";
			secondInstalled.installations[0].target = secondAvailable.solutions[0];
			secondInstalled.installations[0].grantedTeams = [];
			secondInstalled.installations[0].inheritedTeams = [];
			const reads = serve({
				orgId: "org-2",
				availableBody: secondAvailable,
				installationsBody: secondInstalled,
				rolesBody: {
					roles: [
						{ id: "role-org-2", orgId: "org-2", name: "Organization two role" },
					],
				},
				teamsBody: {
					teams: [
						{ id: "team-org-2", orgId: "org-2", name: "Organization two team" },
					],
				},
			});
			const grant = vi.fn();
			server.use(
				http.post(
					rpc("InstallationService", "GrantInstallationToTeam"),
					async ({ request }) => {
						await assertBody(request, {
							orgId: "org-2",
							installationId: "installation-org-2",
							teamId: "team-org-2",
							roleId: "role-org-2",
						});
						grant();
						return HttpResponse.json({ grant: { id: "grant-org-2" } });
					},
				),
			);
			session.organizationId = "org-2";
			rerender(
				<QueryClientProvider client={client}>
					<Page />
				</QueryClientProvider>,
			);
			await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
			expect(
				within(
					await screen.findByRole("table", {
						name: "Installed modules and solutions",
					}),
				).queryByText("expenses"),
			).toBeNull();
			fireEvent.click(
				await screen.findByRole("button", {
					name: "Grant a team access to payables",
				}),
			);
			const dialog = await screen.findByRole("dialog");
			expect(within(dialog).queryByText("Support")).toBeNull();
			await choose(dialog, "Team", "Organization two team");
			await choose(dialog, "Role", "Organization two role");
			fireEvent.click(within(dialog).getByRole("button", { name: "Grant" }));
			await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
			expect(grant).toHaveBeenCalledOnce();
			expect(reads).toEqual({ available: 2, installed: 2, roles: 1, teams: 1 });
		},
	);
});

describe("host read refusals and retained data", () => {
	it.each([
		["ListAvailableSolutions", "Available modules and solutions"],
		["ListOrganizationInstallations", "Installed modules and solutions"],
	])(
		"shows the host's reason on a cold refusal of %s",
		async (method, table) => {
			serve();
			const reason = `requires org admin or owner role: ${method}`;
			server.use(
				http.post(rpc("InstallationService", method), async ({ request }) => {
					await assertBody(request, { orgId: "org-1", pageSize: 500 });
					return refusal(reason);
				}),
			);
			renderInApp(<Page />);
			expect((await screen.findByRole("alert")).textContent).toContain(reason);
			expect(screen.queryByRole("table", { name: table })).toBeNull();
			expect(toast.success).not.toHaveBeenCalled();
		},
	);

	it.each(["permission_denied", "unavailable"])(
		"handles both table refreshes ending in %s with the host's reason",
		async (code) => {
			serve();
			renderInApp(<Page />);
			await screen.findByRole("table", {
				name: "Available modules and solutions",
			});
			await screen.findByRole("table", {
				name: "Installed modules and solutions",
			});
			for (const method of [
				"ListAvailableSolutions",
				"ListOrganizationInstallations",
			]) {
				server.use(
					http.post(rpc("InstallationService", method), async ({ request }) => {
						await assertBody(request, { orgId: "org-1", pageSize: 500 });
						return refusal(`${method} host explanation`, code);
					}),
				);
			}
			fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
			for (const method of [
				"ListAvailableSolutions",
				"ListOrganizationInstallations",
			]) {
				expect(
					await screen.findByText(new RegExp(`${method} host explanation`)),
				).toBeTruthy();
			}
			if (code === "permission_denied") {
				expect(
					screen.queryByRole("table", {
						name: "Available modules and solutions",
					}),
				).toBeNull();
				expect(
					screen.queryByRole("table", {
						name: "Installed modules and solutions",
					}),
				).toBeNull();
			} else {
				expect(
					screen
						.getAllByRole("status")
						.filter((element) => element.textContent?.includes("out of date")),
				).toHaveLength(2);
				expect(
					screen.getByRole("table", {
						name: "Available modules and solutions",
					}),
				).toBeTruthy();
				expect(
					screen.getByRole("table", {
						name: "Installed modules and solutions",
					}),
				).toBeTruthy();
			}
			expect(toast.success).not.toHaveBeenCalled();
		},
	);

	const pickers = [
		{
			kind: "install",
			label: "Agent role",
			service: "PermissionService",
			method: "ListRoles",
			key: ["roles", "org-1"],
			selected: "Expense reader — expenses:read",
			action: "Install",
		},
		{
			kind: "grant",
			label: "Team",
			service: "TeamService",
			method: "ListTeams",
			key: ["teams", "org-1", ""],
			selected: "Support",
			action: "Grant",
		},
		{
			kind: "grant",
			label: "Role",
			service: "PermissionService",
			method: "ListRoles",
			key: ["roles", "org-1"],
			selected: "Solution user — solution:use",
			action: "Grant",
		},
	];
	it.each(pickers)(
		"shows a cold read refusal beside $kind / $label",
		async ({ kind, label, service, method, action }) => {
			serve();
			server.use(
				http.post(rpc(service, method), async ({ request }) => {
					await assertBody(request, { orgId: "org-1" });
					return refusal(`${method}: membership removed`);
				}),
			);
			renderInApp(<Page />);
			fireEvent.click(
				await screen.findByRole("button", {
					name:
						kind === "install"
							? "Install ledger"
							: "Grant a team access to expenses",
				}),
			);
			const dialog = await screen.findByRole("dialog");
			expect((await within(dialog).findByRole("alert")).textContent).toContain(
				`${method}: membership removed`,
			);
			expect(
				(
					within(dialog).getByRole("combobox", {
						name: label,
					}) as HTMLButtonElement
				).disabled,
			).toBe(true);
			expect(
				(
					within(dialog).getByRole("button", {
						name: action,
					}) as HTMLButtonElement
				).disabled,
			).toBe(true);
		},
	);

	it.each(pickers)(
		"withholds $kind / $label and blocks a previously valid selection after refusal",
		async ({ kind, label, service, method, key, selected, action }) => {
			serve();
			const mutation = vi.fn();
			server.use(
				http.post(
					rpc(
						"InstallationService",
						kind === "install" ? "InstallSolution" : "GrantInstallationToTeam",
					),
					() => {
						mutation();
						return HttpResponse.json({});
					},
				),
			);
			const { client } = renderInApp(<Page />);
			const dialog =
				kind === "install" ? await openInstall() : await openGrant();
			server.use(
				http.post(rpc(service, method), async ({ request }) => {
					await assertBody(request, { orgId: "org-1" });
					return refusal(`${method}: permission withdrawn`);
				}),
			);
			await act(async () => {
				await client.invalidateQueries({ queryKey: key });
			});
			expect((await within(dialog).findByRole("alert")).textContent).toContain(
				`${method}: permission withdrawn`,
			);
			const control = within(dialog).getByRole("combobox", { name: label });
			expect((control as HTMLButtonElement).disabled).toBe(true);
			expect(control.textContent).not.toContain(selected);
			fireEvent.click(control);
			expect(screen.queryByRole("option", { name: selected })).toBeNull();
			expect(
				(
					within(dialog).getByRole("button", {
						name: action,
					}) as HTMLButtonElement
				).disabled,
			).toBe(true);
			// Even submitting the form directly cannot reuse the refused selection.
			const form = dialog.querySelector("form");
			if (!form) throw new Error("Form missing");
			await act(async () => {
				fireEvent.submit(form);
			});
			expect(mutation).not.toHaveBeenCalled();
			expect(toast.success).not.toHaveBeenCalled();
		},
	);

	it.each(pickers)(
		"keeps $kind / $label options with a visible stale notice on transient failure",
		async ({ kind, label, service, method, key, selected }) => {
			serve();
			const { client } = renderInApp(<Page />);
			const dialog =
				kind === "install" ? await openInstall() : await openGrant();
			server.use(
				http.post(rpc(service, method), async ({ request }) => {
					await assertBody(request, { orgId: "org-1" });
					return refusal(
						`${method}: host temporarily unavailable`,
						"unavailable",
					);
				}),
			);
			await act(async () => {
				await client.invalidateQueries({ queryKey: key });
			});
			const notice = await within(dialog).findByRole("status");
			expect(notice.textContent).toContain("out of date");
			expect(notice.textContent).toContain(
				`${method}: host temporarily unavailable`,
			);
			await choose(dialog, label, selected);
		},
	);
});

describe("mutation results belong to the page", () => {
	it.each([
		["install", "Cancel"],
		["install", "Close"],
		["install", "Escape"],
		["grant", "Cancel"],
		["grant", "Close"],
		["grant", "Escape"],
	])(
		"shows a delayed %s refusal after %s dismissal without success",
		async (kind, dismiss) => {
			const reads = serve();
			let finish!: () => void;
			const pending = new Promise<void>((resolve) => {
				finish = resolve;
			});
			const received = vi.fn();
			const message =
				'host refuses <img src="x" onerror="alert(1)">: authority changed';
			server.use(
				http.post(
					rpc(
						"InstallationService",
						kind === "install" ? "InstallSolution" : "GrantInstallationToTeam",
					),
					async ({ request }) => {
						await assertBody(
							request,
							kind === "install"
								? {
										orgId: "org-1",
										targetId: LEDGER_TARGET,
										approval: LEDGER_APPROVAL,
										agentIdentifier: "acme/ledger:0.9.1",
										roleId: READER_ROLE,
									}
								: {
										orgId: "org-1",
										installationId: EXPENSES_INSTALLATION,
										teamId: SUPPORT_TEAM,
										roleId: USE_ROLE,
									},
						);
						received();
						await pending;
						return refusal(message);
					},
				),
			);
			renderInApp(<Page />);
			const dialog =
				kind === "install" ? await openInstall() : await openGrant();
			fireEvent.click(
				within(dialog).getByRole("button", {
					name: kind === "install" ? "Install" : "Grant",
				}),
			);
			await waitFor(() => expect(received).toHaveBeenCalledOnce());
			try {
				if (dismiss === "Escape")
					fireEvent.keyDown(dialog, { key: "Escape", code: "Escape" });
				else
					fireEvent.click(
						within(dialog).getByRole("button", { name: dismiss }),
					);
				await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
				expect(toast.success).not.toHaveBeenCalled();
			} finally {
				finish();
			}
			await waitFor(() =>
				expect(toast.error).toHaveBeenCalledExactlyOnceWith(
					`The host refused the ${kind}: ${message}`,
				),
			);
			expect(toast.success).not.toHaveBeenCalled();
			expect(reads.available).toBe(2);
			expect(reads.installed).toBe(2);
			expect(
				(
					screen.getByRole("button", {
						name: "Install ledger",
					}) as HTMLButtonElement
				).disabled,
			).toBe(false);
			expect(
				screen.queryByRole("button", { name: "Remove Support from expenses" }),
			).toBeNull();
		},
	);

	it("refreshes both reads after witnessed removal and reports only that role grant removed", async () => {
		const availableBody = structuredClone(available);
		const installationsBody = structuredClone(installations);
		// The same team also has inherited access, which this action cannot remove.
		installationsBody.installations[0].inheritedTeams[0].subjectLabel =
			"Finance";
		installationsBody.installations[0].inheritedTeams[0].grant.subjectId =
			FINANCE_TEAM;
		const reads = serve({ availableBody, installationsBody });
		server.use(
			http.post(
				rpc("InstallationService", "RevokeInstallationFromTeam"),
				async ({ request }) => {
					await assertBody(request, {
						orgId: "org-1",
						installationId: EXPENSES_INSTALLATION,
						teamId: FINANCE_TEAM,
						roleId: USE_ROLE,
					});
					installationsBody.installations[0].grantedTeams = [];
					availableBody.solutions[0].appliedGeneration = "4";
					return HttpResponse.json({});
				},
			),
		);
		renderInApp(<Page />);
		fireEvent.click(
			await screen.findByRole("button", {
				name: "Remove Finance from expenses",
			}),
		);
		await waitFor(() =>
			expect(toast.success).toHaveBeenCalledExactlyOnceWith(
				"Finance's Solution user grant removed from expenses",
			),
		);
		expect(reads.available).toBe(2);
		expect(reads.installed).toBe(2);
		expect(
			within(
				screen.getByRole("table", { name: "Available modules and solutions" }),
			).getByText("Generation 4 (opened at 2)"),
		).toBeTruthy();
		expect(
			screen.queryByRole("button", { name: "Remove Finance from expenses" }),
		).toBeNull();
		expect(screen.getByText("Inherited from above: Finance")).toBeTruthy();
	});

	it("refreshes both reads after a witnessed uninstall", async () => {
		const availableBody = structuredClone(available);
		const installationsBody = structuredClone(installations);
		const reads = serve({ availableBody, installationsBody });
		server.use(
			http.post(
				rpc("InstallationService", "UninstallSolution"),
				async ({ request }) => {
					await assertBody(request, {
						orgId: "org-1",
						installationId: EXPENSES_INSTALLATION,
					});
					installationsBody.installations = [];
					availableBody.solutions[0].installed = false;
					return HttpResponse.json({});
				},
			),
		);
		renderInApp(<Page />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Uninstall expenses" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		fireEvent.click(within(dialog).getByRole("button", { name: "Uninstall" }));
		await waitFor(() =>
			expect(toast.success).toHaveBeenCalledExactlyOnceWith(
				"expenses uninstalled",
			),
		);
		expect(reads.available).toBe(2);
		expect(reads.installed).toBe(2);
		expect(
			screen.getByText(
				"Nothing is installed in this organization. Once a module or solution is available above, choose it to install it.",
			),
		).toBeTruthy();
		expect(
			screen.queryByRole("button", { name: "Uninstall expenses" }),
		).toBeNull();
		expect(
			(
				screen.getByRole("button", {
					name: "Install expenses",
				}) as HTMLButtonElement
			).disabled,
		).toBe(false);
	});
});

describe("accessible validation and host text", () => {
	it.each([
		["Agent identifier", "", "An agent identifier is required"],
		["Agent identifier", "x".repeat(513), "At most 512 characters"],
		["Agent name (optional)", "x".repeat(513), "At most 512 characters"],
		[
			"Access boundary name (optional)",
			"x".repeat(513),
			"At most 512 characters",
		],
	])(
		"explains invalid %s and focuses its associated error",
		async (label, value, message) => {
			serve();
			const install = vi.fn();
			server.use(
				http.post(rpc("InstallationService", "InstallSolution"), () => {
					install();
					return HttpResponse.json({});
				}),
			);
			renderInApp(<Page />);
			const dialog = await openInstall();
			const control = within(dialog).getByLabelText(label);
			fireEvent.change(control, { target: { value } });
			fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));
			expect((await within(dialog).findByRole("alert")).textContent).toBe(
				message,
			);
			expectFieldError(control, message);
			await waitFor(() => expect(document.activeElement).toBe(control));
			expect(install).not.toHaveBeenCalled();
			expect(toast.success).not.toHaveBeenCalled();
		},
	);

	it.each(["install", "grant"])(
		"announces missing %s picker values and focuses the first invalid control",
		async (kind) => {
			serve();
			const mutation = vi.fn();
			server.use(
				http.post(
					rpc(
						"InstallationService",
						kind === "install" ? "InstallSolution" : "GrantInstallationToTeam",
					),
					() => {
						mutation();
						return HttpResponse.json({});
					},
				),
			);
			renderInApp(<Page />);
			fireEvent.click(
				await screen.findByRole("button", {
					name:
						kind === "install"
							? "Install ledger"
							: "Grant a team access to expenses",
				}),
			);
			const dialog = await screen.findByRole("dialog");
			const submit = within(dialog).getByRole("button", {
				name: kind === "install" ? "Install" : "Grant",
			});
			await waitFor(() =>
				expect((submit as HTMLButtonElement).disabled).toBe(false),
			);
			fireEvent.click(submit);
			await within(dialog).findAllByRole("alert");
			const first = within(dialog).getByRole("combobox", {
				name: kind === "install" ? "Agent role" : "Team",
			});
			if (kind === "install")
				expectFieldError(
					first,
					"Choose the role the solution's agent is granted",
				);
			else {
				expectFieldError(first, "Choose a team");
				expectFieldError(
					within(dialog).getByRole("combobox", { name: "Role" }),
					"Choose the role the team is granted",
				);
			}
			await waitFor(() => expect(document.activeElement).toBe(first));
			expect(mutation).not.toHaveBeenCalled();
		},
	);

	it("renders hostile host names and refusal details as text in lists and controls", async () => {
		const hostile = '<img src="x" onerror="alert(1)">';
		const availableBody = structuredClone(available);
		availableBody.solutions[1].routeAlias = hostile;
		const installationsBody = structuredClone(installations);
		installationsBody.installations[0].grantedTeams[0].subjectLabel = hostile;
		serve({
			availableBody,
			installationsBody,
			rolesBody: {
				roles: [{ id: READER_ROLE, orgId: "org-1", name: hostile }],
			},
		});
		server.use(
			http.post(
				rpc("InstallationService", "InstallSolution"),
				async ({ request }) => {
					await assertBody(request, {
						orgId: "org-1",
						targetId: LEDGER_TARGET,
						approval: LEDGER_APPROVAL,
						agentIdentifier: "acme/ledger:0.9.1",
						roleId: READER_ROLE,
					});
					return refusal(`Cannot accept ${hostile}`);
				},
			),
		);
		renderInApp(<Page />);
		expect(
			within(
				await screen.findByRole("table", {
					name: "Available modules and solutions",
				}),
			).getByText(hostile),
		).toBeTruthy();
		expect(
			within(
				await screen.findByRole("table", {
					name: "Installed modules and solutions",
				}),
			).getByText(hostile),
		).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: `Install ${hostile}` }));
		const dialog = await screen.findByRole("dialog");
		await choose(dialog, "Agent role", hostile);
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));
		expect((await within(dialog).findByRole("alert")).textContent).toContain(
			`Cannot accept ${hostile}`,
		);
		expect(document.querySelector("img, script")).toBeNull();
		expect(toast.success).not.toHaveBeenCalled();
	});
});

// HOST-INST-002, HOST-INST-004, HOST-INST-005: no grants while unacknowledged;
// a refusal remains visible and Retry reuses the same installation identity.
it("shows pending and failed installations and retries the failed step", async () => {
	let retried = false;
	const failed = structuredClone(installations);
	failed.installations[0].installation.status = "INSTALLATION_STATUS_FAILED";
	Object.assign(failed.installations[0].installation, {
		failureReason: "Example setup refused",
	});
	failed.installations[0].grantedTeams = [];
	failed.installations[0].inheritedTeams = [];
	server.use(
		http.post(rpc("InstallationService", "ListAvailableSolutions"), () =>
			HttpResponse.json(available),
		),
		http.post(
			rpc("InstallationService", "ListOrganizationInstallations"),
			() => {
				if (retried) {
					const pending = structuredClone(failed);
					pending.installations[0].installation.status =
						"INSTALLATION_STATUS_PENDING";
					Object.assign(pending.installations[0].installation, {
						failureReason: "",
					});
					return HttpResponse.json(pending);
				}
				return HttpResponse.json(failed);
			},
		),
		http.post(
			rpc("InstallationService", "RetryInstallation"),
			async ({ request }) => {
				await assertBody(request, {
					orgId: "org-1",
					installationId: EXPENSES_INSTALLATION,
				});
				retried = true;
				return HttpResponse.json({
					...failed.installations[0].installation,
					status: "INSTALLATION_STATUS_PENDING",
					failureReason: "",
				});
			},
		),
	);
	renderInApp(<Page />);
	expect(await screen.findByText("Example setup refused")).toBeTruthy();
	expect(screen.queryByRole("button", { name: /grant.*team/i })).toBeNull();
	fireEvent.click(screen.getByRole("button", { name: "Retry expenses" }));
	expect(await screen.findByText("Pending")).toBeTruthy();
	expect(screen.queryByRole("button", { name: "Retry expenses" })).toBeNull();
});
