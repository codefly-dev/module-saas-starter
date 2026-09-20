import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { RoleDetailPage } from "./role-detail-page";

const session = vi.hoisted(() => ({
	organizationId: "org-1" as string | undefined,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: vi.fn(async () => undefined),
}));

vi.mock("@/lib/auth", () => ({ useAuth: () => session }));

const editor = {
	id: "role-1",
	name: "Editor",
	description: "Can edit content",
	permissions: [{ resource: "content", action: "write" }],
	builtIn: false,
	orgId: "org-1",
};

function serveRole(role: Record<string, unknown> = editor) {
	server.use(
		http.post(rpc("PermissionService", "ListRoles"), () =>
			HttpResponse.json({ roles: [role] }),
		),
		http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
			HttpResponse.json({ assignments: [] }),
		),
		http.post(rpc("IntrospectionService", "GetServiceInfo"), () =>
			HttpResponse.json({
				capabilities: {
					permissions: [
						{
							resource: "content",
							action: "write",
							description: "Edit content.",
						},
						{ resource: "users", action: "read", description: "List users." },
					],
				},
			}),
		),
	);
}

beforeEach(() => {
	session.organizationId = "org-1";
	session.orgRole = "owner";
	session.platformRole = undefined;
});

afterEach(cleanup);

describe("RoleDetailPage", () => {
	// The editor sends the set the role should end up with, so an unchanged
	// grant has to travel with the added one — dropping it would be a silent
	// revocation on every save.
	it("saves the whole permission set, not just the change", async () => {
		const updates: unknown[] = [];
		serveRole();
		server.use(
			http.post(rpc("PermissionService", "UpdateRole"), async ({ request }) => {
				updates.push(await request.json());
				return HttpResponse.json({ role: editor });
			}),
		);

		renderInApp(<RoleDetailPage roleId="role-1" />);

		await screen.findByRole("heading", { name: "Editor" });
		fireEvent.click(await screen.findByLabelText("users:read"));
		fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

		await waitFor(() => expect(updates).toHaveLength(1));
		expect(updates[0]).toEqual({
			id: "role-1",
			orgId: "org-1",
			description: "Can edit content",
			permissions: [
				{ resource: "content", action: "write" },
				{ resource: "users", action: "read" },
			],
		});
	});

	it("offers no save control for a built-in role", async () => {
		serveRole({ ...editor, name: "admin", builtIn: true });

		renderInApp(<RoleDetailPage roleId="role-1" />);

		await screen.findByRole("heading", { name: "admin" });
		expect(screen.queryByRole("button", { name: /save changes/i })).toBeNull();
	});

	it("lists principals and teams holding the role", async () => {
		serveRole();
		server.use(
			http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
				HttpResponse.json({
					assignments: [
						{
							id: "a-1",
							subjectId: "user-1",
							subjectKind: 1,
							roleId: "role-1",
						},
						{
							id: "a-2",
							subjectId: "team-1",
							subjectKind: 2,
							roleId: "role-1",
						},
					],
				}),
			),
			http.post(rpc("TeamService", "ListTeams"), () =>
				HttpResponse.json({ teams: [{ id: "team-1", name: "platform" }] }),
			),
		);

		renderInApp(<RoleDetailPage roleId="role-1" />);

		expect(await screen.findByText("platform")).toBeTruthy();
		expect(screen.getByText("Team")).toBeTruthy();
		expect(screen.getByText("Principal")).toBeTruthy();
	});

	it("says so rather than rendering an empty shell when the role is not visible", async () => {
		serveRole();

		renderInApp(<RoleDetailPage roleId="role-missing" />);

		expect(await screen.findByText("Role not found")).toBeTruthy();
	});
});
