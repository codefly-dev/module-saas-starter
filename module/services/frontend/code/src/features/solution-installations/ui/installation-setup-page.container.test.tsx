import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { InstallationSetupPage } from "./installation-setup-page";

const session = vi.hoisted(() => ({
	organizationId: "org-1",
	isAuthenticated: true,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: async () => {},
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => session }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const SUITE_TARGET = "aaaaaaaa-0000-4000-8000-000000000011";
const DOCS_TARGET = "aaaaaaaa-0000-4000-8000-000000000012";
const SUITE_INSTALLATION = "bbbbbbbb-0000-4000-8000-000000000011";
const DOCS_INSTALLATION = "bbbbbbbb-0000-4000-8000-000000000012";
const USE_ROLE = "cccccccc-0000-4000-8000-000000000011";
const SUPPORT_TEAM = "eeeeeeee-0000-4000-8000-000000000011";

const suiteTarget = {
	targetId: SUITE_TARGET,
	bindingId: "binding-suite",
	routeAlias: "suite",
	releasePublisher: "acme",
	releaseName: "suite",
	releaseVersion: "2.0.0",
	openedGeneration: "4",
	appliedGeneration: "4",
	installed: true,
	kind: "solution",
};

const docsTarget = {
	...suiteTarget,
	targetId: DOCS_TARGET,
	bindingId: "binding-documents",
	routeAlias: "documents",
	releaseName: "documents",
	kind: "module",
};

function installation(overrides: Record<string, unknown> = {}) {
	return {
		installation: {
			id: SUITE_INSTALLATION,
			orgId: "org-1",
			targetId: SUITE_TARGET,
			status: "INSTALLATION_STATUS_ACTIVE",
			createdAt: "2026-10-01T00:00:00Z",
		},
		health: "INSTALLATION_HEALTH_HEALTHY",
		target: suiteTarget,
		rootScopePath: "root_wiki",
		grantedTeams: [],
		inheritedTeams: [],
		...overrides,
	};
}

const docsInstallation = {
	installation: {
		id: DOCS_INSTALLATION,
		orgId: "org-1",
		targetId: DOCS_TARGET,
		status: "INSTALLATION_STATUS_ACTIVE",
		createdAt: "2026-10-01T00:00:00Z",
	},
	health: "INSTALLATION_HEALTH_HEALTHY",
	target: docsTarget,
	rootScopePath: "root_documents",
	grantedTeams: [],
	inheritedTeams: [],
	readiness: { ready: true, unmet: [] },
};

const roles = {
	roles: [
		{
			id: USE_ROLE,
			name: "Solution user",
			orgId: "org-1",
			permissions: [{ resource: "solution", action: "use" }],
		},
	],
};
const teams = {
	teams: [{ id: SUPPORT_TEAM, orgId: "org-1", name: "Support" }],
};

function serve(installationsBody: JsonBodyType) {
	const grants: unknown[] = [];
	server.use(
		http.post(rpc("InstallationService", "ListOrganizationInstallations"), () =>
			HttpResponse.json(installationsBody),
		),
		http.post(rpc("PermissionService", "ListRoles"), () =>
			HttpResponse.json(roles),
		),
		http.post(rpc("TeamService", "ListTeams"), () => HttpResponse.json(teams)),
		http.post(
			rpc("InstallationService", "GrantInstallationToTeam"),
			async ({ request }) => {
				grants.push(await request.json());
				// The host answers with what the grant CARRIED and what it could
				// not, which is what the page shows next.
				return HttpResponse.json({
					grant: { id: `grant-${grants.length}` },
					carried: [
						{
							audience: "documents",
							installationId: DOCS_INSTALLATION,
							scopePath: "org.docs",
							roleId: "derived-1",
							scopes: ["evidence:read"],
						},
					],
					unmet: [
						{
							kind: "READINESS_CONDITION_KIND_CARRIED_MODULE_ACCESS_NOT_DERIVED",
							subject: "notes",
							detail: "This organisation has not installed notes.",
							clearedBy: "READINESS_CLEARED_BY_ORGANIZATION_ADMIN",
							action: "READINESS_ACTION_NONE_AT_THIS_SURFACE",
						},
					],
				});
			},
		),
	);
	return grants;
}

// The route page is the server shell: it awaits its params and gates on the
// same permission as the listing. The tests drive the client component it
// renders, as every other admin detail page's tests do.
function render() {
	return renderInApp(
		<InstallationSetupPage installationId={SUITE_INSTALLATION} />,
	);
}

beforeEach(() => {
	session.organizationId = "org-1";
	session.isAuthenticated = true;
	session.orgRole = "owner";
	vi.clearAllMocks();
});
afterEach(cleanup);

// The whole bug: the record is healthy and the thing does not work. The page
// says so, names the binding, says who can clear it, and does NOT offer an act
// that cannot work.
it("reports an omitted binding over a healthy record, with its owner and no false action", async () => {
	serve({
		installations: [
			installation({
				readiness: {
					ready: false,
					unmet: [
						{
							kind: "READINESS_CONDITION_KIND_OPERATION_BINDING_OMITTED",
							subject: "runtime-admission",
							detail:
								'The applied manifest of "documents" asks for operation binding "runtime-admission" and the host derived nothing for it.',
							clearedBy: "READINESS_CLEARED_BY_PLATFORM_OPERATOR",
							action: "READINESS_ACTION_NONE_AT_THIS_SURFACE",
						},
					],
				},
			}),
		],
	});
	render();

	expect(
		await screen.findByText("A declared operation binding derived nothing"),
	).toBeTruthy();
	expect(await screen.findByText("runtime-admission")).toBeTruthy();
	expect(
		await screen.findByText("Cleared by: A platform operator"),
	).toBeTruthy();
	expect(
		await screen.findByText(
			"No action here clears this. The reason above says where it is cleared.",
		),
	).toBeTruthy();
	// Health is still healthy on the same page, and readiness disagrees with it.
	expect(await screen.findByLabelText("Readiness")).toBeTruthy();
	expect(await screen.findByText("Not ready — 1 unmet condition")).toBeTruthy();
});

// A source condition is cleared by the reader, at the surface that owns
// reconnecting a source.
it("sends a source with no delegation to the surface that reconnects it", async () => {
	serve({
		installations: [
			installation({
				readiness: {
					ready: false,
					unmet: [
						{
							kind: "READINESS_CONDITION_KIND_SOURCE_NOT_DELEGATED",
							subject: "acme/connected",
							sourceId: "source-1",
							detail: "The source has no active delegation to documents.",
							clearedBy: "READINESS_CLEARED_BY_VIEWER",
							action: "READINESS_ACTION_RECONNECT_SOURCE",
						},
					],
				},
			}),
		],
	});
	render();

	expect(await screen.findByText("Cleared by: You")).toBeTruthy();
	const link = await screen.findByRole("link", {
		name: "Reconnect this source",
	});
	expect(link.getAttribute("href")).toBe("/admin/datasources");
});

// The four refusals on a validation cell. A grant here is ONE act: it carries
// the access the solution declares it consumes on every module it reaches, so no
// per-module step is offered, and the page reports what was carried and where
// nothing could be — in the host's own words, from the grant's own response.
it("grants once, and reports what that grant carried", async () => {
	const grants = serve({
		installations: [
			installation({
				readiness: { ready: true, unmet: [] },
				reachedModules: [
					{
						audience: "documents",
						installationId: DOCS_INSTALLATION,
						routeAlias: "documents",
					},
					{ audience: "notes", installationId: "", routeAlias: "" },
				],
			}),
			docsInstallation,
		],
	});
	render();

	expect(
		await screen.findByText(
			"A grant here also carries the modules this reaches",
		),
	).toBeTruthy();

	fireEvent.click(await screen.findByRole("button", { name: "Grant a team" }));
	const dialog = await screen.findByRole("dialog");
	// No per-module choice is offered: one grant, one act.
	expect(within(dialog).queryAllByRole("checkbox")).toEqual([]);

	for (const [label, option] of [
		["Team", "Support"],
		["Role", "Solution user — solution:use"],
	] as const) {
		const control = within(dialog).getByRole("combobox", { name: label });
		await waitFor(() =>
			expect((control as HTMLButtonElement).disabled).toBe(false),
		);
		fireEvent.click(control);
		const item = await screen.findByRole("option", { name: option });
		fireEvent.pointerDown(item, { pointerType: "mouse" });
		fireEvent.click(item);
	}
	fireEvent.click(within(dialog).getByRole("button", { name: "Grant" }));

	await waitFor(() => expect(grants.length).toBe(1));
	expect(grants).toEqual([
		{
			orgId: "org-1",
			installationId: SUITE_INSTALLATION,
			teamId: SUPPORT_TEAM,
			roleId: USE_ROLE,
		},
	]);

	// What it carried, and the one module where it carried nothing — stated here
	// rather than met later as a bare Forbidden on a page.
	expect(await screen.findByText("What that grant carried")).toBeTruthy();
	expect(await screen.findByText("documents: evidence:read")).toBeTruthy();
	expect(
		await screen.findByText(/This organisation has not installed notes\./),
	).toBeTruthy();
});
