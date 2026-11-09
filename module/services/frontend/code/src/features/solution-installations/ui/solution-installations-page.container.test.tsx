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
import Page from "@/app/admin/modules/page";
import SolutionsPage from "@/app/admin/solutions/page";
import { FRONTEND_NAVIGATION } from "@/gen/saas/frontend/v1/plugin_catalog";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { canPresent } from "@/lib/plugins/presentation";
import { renderInApp as renderApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

// Existing explicit module administration retains its owner form. Normal
// solution installation is exercised in the combined-review tests.
const renderInApp = renderApp;

const session = vi.hoisted(() => ({
	organizationId: "org-1",
	isAuthenticated: true,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: async () => {},
}));
const push = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }));
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

const REVIEW_OFFER =
	"http://localhost:3000/host.installation.v1.OfferService/ReviewOffer";
const LIST_OFFERS =
	"http://localhost:3000/host.installation.v1.OfferService/ListOffers";
const EXPENSES_TARGET = "aaaaaaaa-0000-4000-8000-000000000001";
const LEDGER_TARGET = "aaaaaaaa-0000-4000-8000-000000000002";
const EXPENSES_INSTALLATION = "bbbbbbbb-0000-4000-8000-000000000001";
const READER_ROLE = "cccccccc-0000-4000-8000-000000000001";
const USE_ROLE = "cccccccc-0000-4000-8000-000000000002";
const FINANCE_TEAM = "eeeeeeee-0000-4000-8000-000000000001";
const SUPPORT_TEAM = "eeeeeeee-0000-4000-8000-000000000002";

const available = {
	solutions: [
		{
			kind: "module",
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
			kind: "module",
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
		// The authority offers nothing an administrator chooses here: every
		// module declares no roles, so it is a supporting module.
		http.post(LIST_OFFERS, () => HttpResponse.json({ offers: [] })),
		// A module that declares no roles: the authority refuses its offer.
		http.post(REVIEW_OFFER, () =>
			HttpResponse.json(
				{
					code: "not_found",
					message:
						"not a composed solution or a module page declaring roles; a module is installed by composition",
				},
				{ status: 404 },
			),
		),
	);
	return reads;
}

// The same reads with expenses a solution: team exposure, its update and its
// uninstall are a solution's, never a module's (installed by composition). The
// bodies given are marked in place, so a handler's later edits of them show.
function solutionBodies({
	availableBody = structuredClone(available) as JsonBodyType,
	installationsBody = structuredClone(installations) as JsonBodyType,
}: {
	availableBody?: JsonBodyType;
	installationsBody?: JsonBodyType;
} = {}) {
	const available = availableBody as {
		solutions?: { targetId: string; kind: string }[];
	};
	for (const target of available.solutions ?? [])
		if (target.targetId === EXPENSES_TARGET) target.kind = "solution";
	const installed = installationsBody as {
		installations?: { target?: { targetId: string; kind: string } }[];
	};
	for (const row of installed.installations ?? [])
		if (row.target?.targetId === EXPENSES_TARGET) row.target.kind = "solution";
	return { availableBody, installationsBody };
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
		// A module is installed by composition: nothing here installs one.
		expect(
			within(expenses).queryByRole("button", { name: /^Install/ }),
		).toBeNull();
		expect(
			within(ledger).queryByRole("button", { name: /^Install/ }),
		).toBeNull();

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
		// A module is installed by composition and used through its roles:
		// nobody uninstalls it, updates it or grants a team on it here.
		for (const action of [
			"Uninstall expenses",
			"Update expenses",
			"Grant a team access to expenses",
		])
			expect(within(row).queryByRole("button", { name: action })).toBeNull();
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

	it("shows a refused uninstall as the host words it, never as done", async () => {
		let uninstalled: unknown;
		serve(solutionBodies());
		server.use(
			http.post(
				rpc("InstallationService", "UninstallSolution"),
				async ({ request }) => {
					uninstalled = await request.json();
					return HttpResponse.json(
						{
							code: "failed_precondition",
							message:
								"cannot uninstall solution: installation 0190a7c2 is still being installed",
						},
						{ status: 400 },
					);
				},
			),
		);
		renderInApp(<SolutionsPage />);

		fireEvent.click(
			await screen.findByRole("button", { name: "Uninstall expenses" }),
		);
		const confirm = await screen.findByRole("alertdialog");
		fireEvent.click(within(confirm).getByRole("button", { name: "Uninstall" }));

		const refusal = await screen.findByText(
			"cannot uninstall solution: installation 0190a7c2 is still being installed",
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
					name: "Installed solutions",
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
		const reads = serve(solutionBodies({ availableBody, installationsBody }));
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
		renderInApp(<SolutionsPage />);

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
		// The Solutions page names a version, the release behind its title.
		expect(
			within(screen.getByRole("table", { name: "Available solutions" }))
				.getByText("1.4.0")
				.getAttribute("title"),
		).toBe("acme/expenses@1.4.0");
		expect(
			within(
				screen.getByRole("list", { name: "Teams granted expenses" }),
			).getByText("Support"),
		).toBeTruthy();
		expect(
			within(
				screen.getByRole("table", { name: "Installed solutions" }),
			).getByText("Agent disabled"),
		).toBeTruthy();
		expect(toast.success).toHaveBeenCalledExactlyOnceWith(
			"Team grant recorded for expenses",
		);
	});

	it("shows the host's refusal of a grant in its own words", async () => {
		serve(solutionBodies());
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
		renderInApp(<SolutionsPage />);
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
								'cannot revoke installation from team: role "viewer" is not granted to that team here',
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
				'cannot revoke installation from team: role "viewer" is not granted to that team here',
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

	it("does not read without an active organization", async () => {
		session.organizationId = "";
		const reads = serve();
		renderInApp(<Page />);
		expect(
			screen.getByText(
				"Select an organization to manage its modules and solutions",
			),
		).toBeTruthy();
		expect(Object.values(reads)).toEqual([0, 0, 0, 0]);
		// A read that never starts is never shown as one in progress.
		await act(() => new Promise((resolve) => setTimeout(resolve, 400)));
		expect(
			screen.queryByText("Reading which targets declare roles"),
		).toBeNull();
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

	it("discards the grant draft and scopes all reads and the next grant after an organization switch", async () => {
		{
			serve(solutionBodies());
			const { client, rerender } = renderInApp(<SolutionsPage />);
			await openGrant();

			const secondAvailable = structuredClone(available);
			secondAvailable.solutions[0].routeAlias = "payables";
			secondAvailable.solutions[1].routeAlias = "receipts";
			const secondInstalled = structuredClone(installations);
			solutionBodies({
				availableBody: secondAvailable,
				installationsBody: secondInstalled,
			});
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
					<SolutionsPage />
				</QueryClientProvider>,
			);
			await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
			expect(screen.queryByLabelText("Show supporting modules")).toBeNull();
			expect(
				within(
					await screen.findByRole("table", {
						name: "Installed solutions",
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
			// Teams are read for the page, which names who holds each role, and
			// again as the grant dialog opens.
			expect(reads).toEqual({ available: 2, installed: 2, roles: 1, teams: 2 });
		}
	});
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
			serve(solutionBodies());
			server.use(
				http.post(rpc(service, method), async ({ request }) => {
					await assertBody(request, { orgId: "org-1" });
					return refusal(`${method}: membership removed`);
				}),
			);
			renderInApp(<SolutionsPage />);
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
		async ({ label, service, method, key, selected, action }) => {
			serve(solutionBodies());
			const mutation = vi.fn();
			server.use(
				http.post(rpc("InstallationService", "GrantInstallationToTeam"), () => {
					mutation();
					return HttpResponse.json({});
				}),
			);
			const { client } = renderInApp(<SolutionsPage />);
			const dialog = await openGrant();
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
		async ({ label, service, method, key, selected }) => {
			serve(solutionBodies());
			const { client } = renderInApp(<SolutionsPage />);
			const dialog = await openGrant();
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
		["grant", "Cancel"],
		["grant", "Close"],
		["grant", "Escape"],
	])(
		"shows a delayed %s refusal after %s dismissal without success",
		async (kind, dismiss) => {
			const reads = serve(solutionBodies());
			let finish!: () => void;
			const pending = new Promise<void>((resolve) => {
				finish = resolve;
			});
			const received = vi.fn();
			const message =
				'host refuses <img src="x" onerror="alert(1)">: authority changed';
			server.use(
				http.post(
					rpc("InstallationService", "GrantInstallationToTeam"),
					async ({ request }) => {
						await assertBody(request, {
							orgId: "org-1",
							installationId: EXPENSES_INSTALLATION,
							teamId: SUPPORT_TEAM,
							roleId: USE_ROLE,
						});
						received();
						await pending;
						return refusal(message);
					},
				),
			);
			renderInApp(<SolutionsPage />);
			const dialog = await openGrant();
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
		const reads = serve(solutionBodies({ availableBody, installationsBody }));
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
		renderInApp(<SolutionsPage />);
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
				"Nothing is installed in this organization. Once a solution or module page is available above, choose it to install it.",
			),
		).toBeTruthy();
		expect(
			screen.queryByRole("button", { name: "Uninstall expenses" }),
		).toBeNull();
	});
});

describe("accessible validation and host text", () => {
	it.each(["grant"])(
		"announces missing %s picker values and focuses the first invalid control",
		async (kind) => {
			serve(solutionBodies());
			const mutation = vi.fn();
			server.use(
				http.post(rpc("InstallationService", "GrantInstallationToTeam"), () => {
					mutation();
					return HttpResponse.json({});
				}),
			);
			renderInApp(<SolutionsPage />);
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

	it("renders hostile host names as text in lists", async () => {
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
		// Supporting module administration lists a module once the authority
		// says it declares no roles.
		http.post(LIST_OFFERS, () => HttpResponse.json({ offers: [] })),
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
	expect(await screen.findByText("Being set up")).toBeTruthy();
	expect(screen.queryByRole("button", { name: "Retry expenses" })).toBeNull();
});

describe("Admin → Solutions lists solutions; module pages appear only as set-up entries", () => {
	const PAYROLL_TARGET = "aaaaaaaa-0000-4000-8000-000000000003";
	const DIRECTORY_TARGET = "aaaaaaaa-0000-4000-8000-000000000004";
	const ARCHIVE_TARGET = "aaaaaaaa-0000-4000-8000-000000000005";
	const DIRECTORY_INSTALLATION = "bbbbbbbb-0000-4000-8000-000000000004";
	// A module page: a module declaring roles (AvailableSolution.roles).
	const page = { ...available.solutions[1], roles: ["installer", "viewer"] };
	const catalogue = {
		solutions: [
			{
				...available.solutions[1],
				kind: "solution",
				targetId: PAYROLL_TARGET,
				routeAlias: "payroll",
				releaseName: "payroll",
			},
			page,
			// A module without roles.
			available.solutions[0],
			{
				...page,
				targetId: DIRECTORY_TARGET,
				routeAlias: "directory",
				releaseName: "directory",
				installed: true,
			},
			{
				...page,
				targetId: ARCHIVE_TARGET,
				routeAlias: "archive",
				releaseName: "archive",
			},
		],
	};
	const installed = {
		installations: [
			installations.installations[0],
			{
				...installations.installations[0],
				installation: {
					...installations.installations[0].installation,
					id: DIRECTORY_INSTALLATION,
					targetId: DIRECTORY_TARGET,
				},
				target: catalogue.solutions[3],
				grantedTeams: [],
				inheritedTeams: [],
			},
		],
	};

	// The authority offers solutions alone, and a solution is listed as the
	// host delivers it; a failed read is said and read again.
	function serveSolutions({ failsTimes = 0 } = {}) {
		const reads = { offers: 0, setUp: [] as unknown[] };
		serve({ availableBody: catalogue, installationsBody: installed });
		server.use(
			http.post(LIST_OFFERS, () => {
				reads.offers++;
				if (reads.offers <= failsTimes)
					return HttpResponse.json(
						{ code: "unavailable", message: "authority restarting" },
						{ status: 503 },
					);
				return HttpResponse.json({
					offers: [{ target: "payroll", generation: "5" }],
				});
			}),
			http.post(
				rpc("InstallationService", "SetUpModulePage"),
				async ({ request }) => {
					reads.setUp.push(await request.json());
					return HttpResponse.json({
						id: "bbbbbbbb-0000-4000-8000-000000000002",
						orgId: "org-1",
						targetId: LEDGER_TARGET,
						status: "INSTALLATION_STATUS_ACTIVE",
					});
				},
			),
		);
		return reads;
	}

	it("lists solutions to install and module pages to set up, never a module page to install", async () => {
		const reads = serveSolutions({ failsTimes: 1 });
		renderInApp(<SolutionsPage />);

		const availableTable = await screen.findByRole("table", {
			name: "Available solutions",
		});
		// A module page needs no offer: it is set up, never installed, and it
		// is listed apart from the solutions.
		const pagesTable = await screen.findByRole("table", {
			name: "Available module pages",
		});
		for (const alias of ["ledger", "directory", "archive"]) {
			await within(pagesTable).findByText(alias);
			expect(within(availableTable).queryByText(alias)).toBeNull();
			expect(
				within(pagesTable).getByRole("button", {
					name: `Set up access to ${alias}`,
				}),
			).toBeTruthy();
			expect(
				screen.queryByRole("button", {
					name: `Install ${alias}`,
				}),
			).toBeNull();
		}
		expect(within(availableTable).queryByText("expenses")).toBeNull();
		await within(availableTable).findByText("payroll");
		expect(
			within(availableTable).getByRole("button", { name: "Install payroll" }),
		).toBeTruthy();
		const banner = await screen.findByText(
			"The targets this organization may choose could not be read",
		);
		expect(screen.getByText(/authority restarting/)).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: "Try again" }));
		await waitFor(() => expect(banner.isConnected).toBe(false));

		const installedTable = await screen.findByRole("table", {
			name: "Installed solutions",
		});
		await within(installedTable).findByText("directory");
		expect(within(installedTable).queryByText("expenses")).toBeNull();
		expect(reads.offers).toBe(2);
	});

	it("a module page is set up from Admin in one click", async () => {
		const reads = serveSolutions();
		renderInApp(<SolutionsPage />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Set up access to ledger" }),
		);
		const dialog = await screen.findByRole("dialog");
		await within(dialog).findByRole("region", { name: "Roles" });
		const me = (role: string) =>
			within(
				within(dialog).getByRole("region", { name: `Role ${role}` }),
			).getByRole("checkbox", {
				name: "Add me to this team",
			}) as HTMLInputElement;
		// Every role ticked for the administrator; nothing must be picked.
		for (const role of ["installer", "viewer"])
			expect(me(role).checked).toBe(true);
		fireEvent.click(me("installer"));
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Set up access" }),
		);
		await waitFor(() => expect(reads.setUp).toHaveLength(1));
		expect(reads.setUp[0]).toEqual({
			orgId: "org-1",
			targetId: LEDGER_TARGET,
			installerRoles: ["viewer"],
		});
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(toast.success).toHaveBeenCalledWith(
			"Access to ledger is set up. Each role's team holds it now.",
		);
	});

	it("a module page someone holds a role on reads Set up, and offers Manage access instead", async () => {
		serveSolutions();
		const pageInstalled = structuredClone(installed);
		pageInstalled.installations[1].rootScopePath = "";
		serve({ availableBody: catalogue, installationsBody: pageInstalled });
		server.use(
			http.post(LIST_OFFERS, () =>
				HttpResponse.json({ offers: [{ target: "payroll", generation: "5" }] }),
			),
			http.post(
				"http://localhost:3000/host.installation.v1.InstallationService/List",
				() =>
					HttpResponse.json({
						installations: [
							{ id: DIRECTORY_INSTALLATION, roles: ["installer", "viewer"] },
						],
					}),
			),
			http.post(
				"http://localhost:3000/host.installation.v1.InstallationService/ListRoleGrants",
				() =>
					HttpResponse.json({
						grants: [{ role: "viewer", teamId: FINANCE_TEAM }],
					}),
			),
		);
		renderInApp(<SolutionsPage />);
		const pagesTable = await screen.findByRole("table", {
			name: "Available module pages",
		});
		const directory = (
			await within(pagesTable).findByText("directory")
		).closest("tr") as HTMLElement;
		const manage = await within(directory).findByRole("link", {
			name: "Manage access to directory",
		});
		expect(manage.getAttribute("href")).toBe(
			`/admin/solutions/${DIRECTORY_INSTALLATION}`,
		);
		expect(within(directory).getByText("Set up")).toBeTruthy();
		expect(
			within(directory).queryByRole("button", {
				name: "Set up access to directory",
			}),
		).toBeNull();
		// A page nobody holds a role on is still set up from here.
		expect(
			within(pagesTable).getByRole("button", {
				name: "Set up access to ledger",
			}),
		).toBeTruthy();
		// The installed row names who holds each role, by team name.
		const holders = await screen.findByRole("list", {
			name: "Who has access to directory",
		});
		expect(holders.textContent).toContain("viewer: Finance");
		expect(holders.textContent).toContain("installer: nobody");
		// Its install date sits under its name.
		const installedRow = holders.closest("tr") as HTMLElement;
		expect(
			within(installedRow).getByText(/^Installed (Sep 30|Oct 1), 2026/),
		).toBeTruthy();
	});

	it("offers no module on supporting module administration until the offers are read, and says a failed read as the host words it", async () => {
		const reads = serve({
			availableBody: catalogue,
			installationsBody: installed,
		});
		let release: () => void = () => {};
		const answered = new Promise<void>((resolve) => {
			release = resolve;
		});
		let offerReads = 0;
		server.use(
			http.post(LIST_OFFERS, async () => {
				offerReads++;
				if (offerReads === 1) {
					await answered;
					return HttpResponse.json(
						{ code: "unavailable", message: "authority restarting" },
						{ status: 503 },
					);
				}
				return HttpResponse.json({
					offers: [{ target: "payroll", generation: "5" }],
				});
			}),
		);
		renderInApp(<Page />);
		await waitFor(() => {
			expect(reads.available).toBe(1);
			expect(reads.installed).toBe(1);
		});
		expect(
			screen.queryByRole("table", { name: "Available modules and solutions" }),
		).toBeNull();
		const installedModules = async () => {
			const table = await screen.findByRole("table", {
				name: "Installed modules and solutions",
			});
			await within(table).findByText("expenses");
			await within(table).findByText("directory");
		};
		await installedModules();

		release();
		expect(await screen.findByText(/authority restarting/)).toBeTruthy();
		expect(screen.getByText(/so no module is listed yet/)).toBeTruthy();
		expect(
			screen.queryByRole("table", { name: "Available modules and solutions" }),
		).toBeNull();

		fireEvent.click(screen.getByRole("button", { name: "Try again" }));
		const availableTable = await screen.findByRole("table", {
			name: "Available modules and solutions",
		});
		// Every module, module pages included, is installed by composition.
		for (const alias of ["expenses", "ledger", "directory", "archive"])
			await within(availableTable).findByText(alias);
		expect(within(availableTable).queryByText("payroll")).toBeNull();
		await installedModules();
		expect(offerReads).toBe(2);
	});
});

describe("an installation survives delivery: the host offers an update, and the review installs it in place", () => {
	const PAYROLL_TARGET = "aaaaaaaa-0000-4000-8000-000000000006";
	const PAYROLL_INSTALLATION = "bbbbbbbb-0000-4000-8000-000000000006";
	const payroll = {
		...available.solutions[1],
		kind: "solution",
		targetId: PAYROLL_TARGET,
		routeAlias: "payroll",
		releaseName: "payroll",
		appliedGeneration: "7",
		installed: true,
	};
	const approval = {
		targetId: "solution:payroll",
		generation: "7",
		digest: "sha256:payroll-7",
		operationId: "cccccccc-0000-4000-8000-000000000007",
	};
	const review = {
		orgId: "org-1",
		actorId: "person-1",
		targetId: PAYROLL_TARGET,
		target: "payroll",
		bindingId: "binding-payroll",
		release: "acme/payroll@0.9.1",
		deliveryDigest: "delivery-7",
		installationRole: "none",
		approval,
		modules: [],
	};
	const collections = (actions: string[]) => ({
		path: ["payroll", "agent", "documents"],
		module: "documents",
		via: "binding",
		scopes: [{ resourceKind: "documents.collection", actions }],
	});
	const installedPayroll = (upgradeAvailable: boolean) => ({
		installations: [
			{
				...installations.installations[0],
				installation: {
					...installations.installations[0].installation,
					id: PAYROLL_INSTALLATION,
					targetId: PAYROLL_TARGET,
					rootScopeNodeId: "",
					approvedGeneration: "5",
					upgradeAvailable,
				},
				target: payroll,
				rootScopePath: "",
				grantedTeams: [],
				inheritedTeams: [],
			},
		],
	});

	it("says an update is available, keeps the installation healthy, and installs the reviewed release on the same installation", async () => {
		let installedWith: unknown;
		serve({
			availableBody: { solutions: [payroll] },
			installationsBody: installedPayroll(true),
		});
		server.use(
			http.post(
				rpc("InstallationService", "ReviewSolutionInstall"),
				async ({ request }) => {
					await assertBody(request, {
						orgId: "org-1",
						targetId: PAYROLL_TARGET,
					});
					// The editor was approved to read collections through the agent.
					return HttpResponse.json({
						...review,
						approvedRoles: [
							{ name: "installer" },
							{ name: "editor", reach: [collections(["read"])] },
						],
					});
				},
			),
			http.post(REVIEW_OFFER, () =>
				HttpResponse.json({
					target: "payroll",
					installationRole: "none",
					approval: { ...approval, operationId: "" },
					roles: [
						{ name: "editor", reach: [collections(["read", "create"])] },
						{ name: "installer", scopes: [] },
					],
					rolesDeclared: true,
					installation: {
						id: PAYROLL_INSTALLATION,
						status: "STATUS_ACTIVE",
						approvedGeneration: "5",
						upgradeAvailable: true,
					},
				}),
			),
			http.post(
				rpc("InstallationService", "InstallReviewedSolution"),
				async ({ request }) => {
					installedWith = await request.json();
					return HttpResponse.json({
						id: PAYROLL_INSTALLATION,
						orgId: "org-1",
						targetId: PAYROLL_TARGET,
						status: "INSTALLATION_STATUS_ACTIVE",
						approvedGeneration: "7",
					});
				},
			),
		);
		renderInApp(<SolutionsPage />);
		const installedTable = await screen.findByRole("table", {
			name: "Installed solutions",
		});
		await within(installedTable).findByText("Update available");
		expect(within(installedTable).getByText("Healthy")).toBeTruthy();
		fireEvent.click(
			within(installedTable).getByRole("button", { name: "Update payroll" }),
		);
		const dialog = await screen.findByRole("dialog");
		await within(dialog).findByText("Update payroll");
		// It names what the update asks beyond what was approved, in words.
		const adds = within(
			await within(dialog).findByRole("region", {
				name: "What this update adds",
			}),
		);
		expect(adds.getByText("An editor would also be able to")).toBeTruthy();
		expect(
			adds.getByText("Through payroll and agent, in documents"),
		).toBeTruthy();
		expect(adds.getByText("Create collections")).toBeTruthy();
		expect(adds.queryByText("Read collections")).toBeNull();
		expect(
			adds.getByText(/everyone keeps exactly the access you approved/),
		).toBeTruthy();
		fireEvent.click(within(dialog).getByRole("checkbox"));
		fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));
		await waitFor(() => expect(installedWith).toBeTruthy());
		expect(installedWith).toMatchObject({
			orgId: "org-1",
			review: {
				targetId: PAYROLL_TARGET,
				approval: { operationId: approval.operationId },
			},
		});
		await waitFor(() =>
			expect(toast.success).toHaveBeenCalledWith(
				"payroll updated. Its team access is kept.",
			),
		);
	});

	it("offers no update while the release in place is the approved one", async () => {
		serve({
			availableBody: { solutions: [payroll] },
			installationsBody: installedPayroll(false),
		});
		renderInApp(<SolutionsPage />);
		const installedTable = await screen.findByRole("table", {
			name: "Installed solutions",
		});
		await within(installedTable).findByText("payroll");
		expect(within(installedTable).queryByText("Update available")).toBeNull();
		expect(
			within(installedTable).queryByRole("button", { name: "Update payroll" }),
		).toBeNull();
	});
});
