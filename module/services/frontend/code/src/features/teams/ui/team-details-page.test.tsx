import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { TeamDetailsPage } from "./team-details-page";

const auth = vi.hoisted(() => ({
	organizationId: "org-example",
	user: { id: "user-example" },
	orgRole: "member",
	platformRole: undefined as string | undefined,
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => auth }));
let role = 1;
let removed = false;
const team = {
	id: "team-example",
	orgId: "org-example",
	name: "Example team",
	description: "Our shared work",
};
beforeEach(() => {
	role = 1;
	removed = false;
	auth.orgRole = "member";
	auth.platformRole = undefined;
	server.use(
		http.post(rpc("TeamService", "ListTeams"), () =>
			HttpResponse.json({ teams: [team] }),
		),
		http.post(rpc("TeamService", "ListMembers"), () =>
			HttpResponse.json({
				members: removed
					? []
					: [
							{
								teamId: team.id,
								userId: auth.user.id,
								userEmail: "jane@example.com",
								role,
							},
						],
			}),
		),
	);
});
afterEach(cleanup);

describe("team details", () => {
	it("shows the roster, named role, and read-only member access", async () => {
		renderInApp(<TeamDetailsPage teamId={team.id} />);
		expect(
			await screen.findByRole("heading", { name: "Example team" }),
		).toBeTruthy();
		expect(
			await screen.findByText("You are not a team administrator."),
		).toBeTruthy();
		expect(screen.getByText("jane@example.com")).toBeTruthy();
		expect(screen.getByText("Team member")).toBeTruthy();
		expect(screen.queryByRole("button", { name: "Edit team" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Add member" })).toBeNull();
		expect(screen.queryByText(team.id)).toBeNull();
	});
	it("lets a team admin manage members without organization-admin status", async () => {
		role = 2;
		renderInApp(<TeamDetailsPage teamId={team.id} />);
		expect(
			await screen.findByText("You are a team administrator."),
		).toBeTruthy();
		expect(screen.getByRole("button", { name: "Edit team" })).toBeTruthy();
		expect(screen.getByRole("button", { name: "Add member" })).toBeTruthy();
	});
	it("distinguishes organization admin access from a team-admin membership", async () => {
		auth.orgRole = "owner";
		removed = true;
		renderInApp(<TeamDetailsPage teamId={team.id} />);
		expect(await screen.findByText("Not a team member")).toBeTruthy();
		expect(
			screen.getByText(
				"Your organization admin access allows you to manage this team.",
			),
		).toBeTruthy();
		expect(screen.getByRole("button", { name: "Add member" })).toBeTruthy();
	});
	it("confirms a role change and refreshes the roster with the new role", async () => {
		auth.orgRole = "owner";
		let body: unknown;
		server.use(
			http.post(rpc("TeamService", "AddMember"), async ({ request }) => {
				body = await request.json();
				role = 2;
				return HttpResponse.json({});
			}),
		);
		renderInApp(<TeamDetailsPage teamId={team.id} />);
		fireEvent.click(
			await screen.findByRole("button", {
				name: "Make admin: jane@example.com",
			}),
		);
		expect(body).toBeUndefined();
		fireEvent.click(
			within(await screen.findByRole("dialog")).getByRole("button", {
				name: "Change role",
			}),
		);
		await screen.findByText("Team admin");
		expect(body).toEqual({
			teamId: team.id,
			userId: auth.user.id,
			role: "TEAM_ROLE_ADMIN",
		});
	});
	it("requires confirmation before removal and updates the roster", async () => {
		auth.orgRole = "owner";
		const remove = vi.fn(() => {
			removed = true;
			return HttpResponse.json({});
		});
		server.use(http.post(rpc("TeamService", "RemoveMember"), remove));
		renderInApp(<TeamDetailsPage teamId={team.id} />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Remove jane@example.com" }),
		);
		expect(remove).not.toHaveBeenCalled();
		fireEvent.click(
			within(await screen.findByRole("dialog")).getByRole("button", {
				name: "Remove member",
			}),
		);
		await screen.findByText("No members in this team yet.");
		expect(remove).toHaveBeenCalledOnce();
	});
	it("keeps failed changes visible in the dialog and preserves the roster", async () => {
		auth.orgRole = "owner";
		server.use(
			http.post(rpc("TeamService", "RemoveMember"), () =>
				HttpResponse.json(
					{
						code: "permission_denied",
						message: "Access changed. Refresh and try again.",
					},
					{ status: 403 },
				),
			),
		);
		renderInApp(<TeamDetailsPage teamId={team.id} />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Remove jane@example.com" }),
		);
		const dialog = await screen.findByRole("dialog");
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Remove member" }),
		);
		expect((await within(dialog).findByRole("alert")).textContent).toContain(
			"Access changed",
		);
	});
	it("does not load a roster for a team outside the selected organization", async () => {
		const roster = vi.fn(() => HttpResponse.json({ members: [] }));
		server.use(http.post(rpc("TeamService", "ListMembers"), roster));
		renderInApp(<TeamDetailsPage teamId="another-team" />);
		await screen.findByText("Team unavailable");
		expect(roster).not.toHaveBeenCalled();
	});
	it("does not mistake a roster failure for an empty team or grant management controls", async () => {
		auth.orgRole = "owner";
		server.use(
			http.post(rpc("TeamService", "ListMembers"), () =>
				HttpResponse.json(
					{ code: "unavailable", message: "Unavailable" },
					{ status: 503 },
				),
			),
		);
		renderInApp(<TeamDetailsPage teamId={team.id} />);
		await waitFor(() =>
			expect(screen.getByRole("alert").textContent).toContain(
				"Couldn't load team members",
			),
		);
		expect(screen.queryByRole("button", { name: "Edit team" })).toBeNull();
	});
});
