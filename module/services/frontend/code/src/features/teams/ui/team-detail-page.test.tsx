import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { TeamDetailPage } from "./team-detail-page";

const session = vi.hoisted(() => ({
	organizationId: "org-1" as string | undefined,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: vi.fn(async () => undefined),
}));

vi.mock("@/lib/auth", () => ({ useAuth: () => session }));

function serveTeam() {
	server.use(
		http.post(rpc("TeamService", "ListTeams"), () =>
			HttpResponse.json({
				teams: [
					{
						id: "team-1",
						name: "platform",
						description: "Runs the platform",
						path: "platform",
						orgId: "org-1",
					},
				],
			}),
		),
		http.post(rpc("TeamService", "ListMembers"), () =>
			HttpResponse.json({
				members: [
					{
						teamId: "team-1",
						userId: "user-1",
						userEmail: "user@example.com",
						role: 1,
					},
				],
			}),
		),
		http.post(rpc("PermissionService", "ListRoles"), () =>
			HttpResponse.json({
				roles: [
					{
						id: "role-1",
						name: "Editor",
						permissions: [],
						builtIn: false,
						orgId: "org-1",
						description: "",
					},
				],
			}),
		),
		http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
			HttpResponse.json({
				assignments: [
					{ id: "a-1", subjectId: "team-1", subjectKind: 2, roleId: "role-1" },
				],
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

describe("TeamDetailPage", () => {
	it("shows the team's members and the roles it carries", async () => {
		serveTeam();

		renderInApp(<TeamDetailPage teamId="team-1" />);

		expect(
			await screen.findByRole("heading", { name: "platform" }),
		).toBeTruthy();
		expect(await screen.findByText("Editor")).toBeTruthy();
	});

	// A team role is revoked from the team, not from each member, so the
	// revoke here has to name the team as the subject.
	it("revokes a team's role with the team as the subject", async () => {
		const revocations: unknown[] = [];
		serveTeam();
		server.use(
			http.post(rpc("PermissionService", "RevokeRole"), async ({ request }) => {
				revocations.push(await request.json());
				return HttpResponse.json({});
			}),
		);

		renderInApp(<TeamDetailPage teamId="team-1" />);

		fireEvent.click(await screen.findByLabelText("Revoke"));

		await waitFor(() => expect(revocations).toHaveLength(1));
		expect(revocations[0]).toEqual({
			subjectId: "team-1",
			roleId: "role-1",
			orgId: "org-1",
		});
	});

	it("removes a member through the team service", async () => {
		const removals: unknown[] = [];
		serveTeam();
		server.use(
			http.post(rpc("TeamService", "RemoveMember"), async ({ request }) => {
				removals.push(await request.json());
				return HttpResponse.json({});
			}),
		);

		renderInApp(<TeamDetailPage teamId="team-1" />);

		fireEvent.click(
			await screen.findByRole("button", { name: "Remove user@example.com" }),
		);
		expect(removals).toHaveLength(0);
		fireEvent.click(
			within(await screen.findByRole("dialog")).getByRole("button", {
				name: "Remove member",
			}),
		);

		await waitFor(() => expect(removals).toHaveLength(1));
		expect(removals[0]).toEqual({ teamId: "team-1", userId: "user-1" });
	});
});
