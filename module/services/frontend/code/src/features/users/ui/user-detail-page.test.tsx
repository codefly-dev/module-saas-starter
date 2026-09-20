import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { UserDetailPage } from "./user-detail-page";

const session = vi.hoisted(() => ({
	organizationId: "org-1" as string | undefined,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: vi.fn(async () => undefined),
}));

vi.mock("@/lib/auth", () => ({ useAuth: () => session }));

const roles = [
	{
		id: "role-direct",
		name: "Editor",
		permissions: [{ resource: "content", action: "write" }],
		builtIn: false,
		orgId: "org-1",
		description: "",
	},
	{
		id: "role-team",
		name: "Auditor",
		permissions: [{ resource: "audit", action: "read" }],
		builtIn: false,
		orgId: "org-1",
		description: "",
	},
];

function serveGrants(
	listedTeamRequests: unknown[] = [],
	assignmentRequests: { subjectId?: string }[] = [],
) {
	server.use(
		http.post(rpc("UserService", "GetUser"), () =>
			HttpResponse.json({ uuid: "user-1", primaryEmail: "jane@example.com" }),
		),
		http.post(rpc("PermissionService", "ListRoles"), () =>
			HttpResponse.json({ roles }),
		),
		http.post(
			rpc("PermissionService", "ListRoleAssignments"),
			async ({ request }) => {
				const body = (await request.json()) as { subjectId?: string };
				assignmentRequests.push(body);
				if (body.subjectId === "user-1") {
					return HttpResponse.json({
						assignments: [
							{
								id: "a-1",
								subjectId: "user-1",
								subjectKind: 1,
								roleId: "role-direct",
								scope: "project-x",
							},
						],
					});
				}
				if (body.subjectId === "team-1") {
					return HttpResponse.json({
						assignments: [
							{
								id: "a-2",
								subjectId: "team-1",
								subjectKind: 2,
								roleId: "role-team",
							},
						],
					});
				}
				return HttpResponse.json({ assignments: [] });
			},
		),
		http.post(rpc("TeamService", "ListTeams"), async ({ request }) => {
			listedTeamRequests.push(await request.json());
			return HttpResponse.json({
				teams: [{ id: "team-1", name: "compliance" }],
			});
		}),
	);
}

beforeEach(() => {
	session.organizationId = "org-1";
	session.orgRole = "owner";
	session.platformRole = undefined;
});

afterEach(cleanup);

describe("UserDetailPage", () => {
	// A permission an administrator cannot trace is one they cannot revoke, so
	// the page has to name the path, not just the permission.
	it("shows both a direct grant and a team grant with their provenance", async () => {
		serveGrants();

		renderInApp(<UserDetailPage userId="user-1" />);

		expect(await screen.findByText("content:write")).toBeTruthy();
		expect(screen.getByText("audit:read")).toBeTruthy();
		expect(screen.getByText("Auditor via compliance")).toBeTruthy();
		// Both the direct-assignment badge and the provenance badge name the
		// scope, because acting on either one without it revokes nothing.
		expect(screen.getAllByText("Editor in project-x")).toHaveLength(2);
	});

	// Without the member filter the page would have to walk every team in the
	// organization to answer "which teams is this person in".
	it("asks for the teams this member belongs to, not the org's whole tree", async () => {
		const teamRequests: unknown[] = [];
		serveGrants(teamRequests);

		renderInApp(<UserDetailPage userId="user-1" />);

		await screen.findByText("compliance");
		expect(teamRequests).toContainEqual({ orgId: "org-1", memberId: "user-1" });
	});

	it("revokes a direct grant in place", async () => {
		const revocations: unknown[] = [];
		serveGrants();
		server.use(
			http.post(rpc("PermissionService", "RevokeRole"), async ({ request }) => {
				revocations.push(await request.json());
				return HttpResponse.json({});
			}),
		);

		renderInApp(<UserDetailPage userId="user-1" />);

		fireEvent.click((await screen.findAllByLabelText("Revoke"))[0]);

		await waitFor(() => expect(revocations).toHaveLength(1));
		expect(revocations[0]).toEqual({
			subjectId: "user-1",
			roleId: "role-direct",
			orgId: "org-1",
			scope: "project-x",
		});
	});

	// One person's page must not cost the whole tenant: the reads are bounded
	// by the subject and the teams they are in, never the organization's entire
	// assignment list.
	it("asks only for this subject's grants and those of their own teams", async () => {
		const assignmentRequests: { subjectId?: string }[] = [];
		serveGrants([], assignmentRequests);

		renderInApp(<UserDetailPage userId="user-1" />);

		await screen.findByText("content:write");
		expect(assignmentRequests.every((body) => !!body.subjectId)).toBe(true);
		expect(new Set(assignmentRequests.map((body) => body.subjectId))).toEqual(
			new Set(["user-1", "team-1"]),
		);
	});
});
