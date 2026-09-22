import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { PermissionsPage } from "./permissions-page";

const session = vi.hoisted(() => ({
	organizationId: "org-1" as string | undefined,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: vi.fn(async () => undefined),
}));

vi.mock("@/lib/auth", () => ({ useAuth: () => session }));

function serveVocabulary() {
	server.use(
		http.post(rpc("IntrospectionService", "GetServiceInfo"), () =>
			HttpResponse.json({
				capabilities: {
					permissions: [
						{ resource: "users", action: "read", description: "List users." },
						{
							resource: "audit",
							action: "read",
							description: "Read audit events.",
						},
					],
				},
			}),
		),
		http.post(rpc("PermissionService", "ListRoles"), () =>
			HttpResponse.json({
				roles: [
					{
						id: "role-admin",
						name: "admin",
						permissions: [{ resource: "*", action: "*" }],
						builtIn: true,
						orgId: "",
						description: "",
					},
					{
						id: "role-auditor",
						name: "auditor",
						permissions: [{ resource: "audit", action: "read" }],
						builtIn: false,
						orgId: "org-1",
						description: "",
					},
				],
			}),
		),
		http.post(rpc("OrganizationService", "ListMembers"), () =>
			HttpResponse.json({ members: [] }),
		),
		http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
			HttpResponse.json({ assignments: [] }),
		),
	);
}

beforeEach(() => {
	session.organizationId = "org-1";
	session.orgRole = "owner";
	session.platformRole = undefined;
});

afterEach(cleanup);

describe("PermissionsPage", () => {
	it("groups the declared vocabulary by the resource it acts on", async () => {
		serveVocabulary();

		renderInApp(<PermissionsPage />);

		expect(await screen.findByRole("heading", { name: "audit" })).toBeTruthy();
		expect(screen.getByRole("heading", { name: "users" })).toBeTruthy();
		expect(screen.getByText("Read audit events.")).toBeTruthy();
	});

	// "Who can do X" has to count the wildcard role, or an administrator reads
	// "only auditor" off a page and revokes the wrong grant.
	it("counts a wildcard role among the roles granting a named permission", async () => {
		serveVocabulary();

		renderInApp(<PermissionsPage />);

		const auditRow = (await screen.findByText("audit:read")).closest("tr");
		expect(auditRow?.textContent).toContain("admin");
		expect(auditRow?.textContent).toContain("auditor");

		const usersRow = screen.getByText("users:read").closest("tr");
		expect(usersRow?.textContent).toContain("admin");
		expect(usersRow?.textContent).not.toContain("auditor");
	});
});
