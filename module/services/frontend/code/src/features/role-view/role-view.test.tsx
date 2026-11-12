import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { rpc } from "@/test/container";
import { server } from "@/test/setup";
import { ActAsTeam, RoleViewBanner } from "./role-view";

// The person on the page holds no role the browser knows of. Nothing here may
// decide who can cast: that is the authority's, and these tests would pass a
// client-side gate only by lying about a role.
const auth = vi.hoisted(() => ({
	isAuthenticated: true,
	accessToken: "own-session",
	organizationId: "org-example",
	user: { id: "user-example", email: "ada@example.com" },
	enterRoleView: vi.fn(),
	exitRoleView: vi.fn(() => Promise.resolve()),
	narrowedSessionEnd: null as { reason: "expired" | "reloaded"; as: string } | null,
	dismissNarrowedSessionEnd: vi.fn(),
}));
// The cast is the provider's reading of the host's Session.cast_team_id; the
// bar reads it from the same place the menus do.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ ...auth, castTeamId: (current.castTeamId as string) ?? "" }),
}));

const session = (method: string) => `http://localhost:3000/host.session.v1.SessionService/${method}`;
let current: Record<string, unknown> = {};
let asked: unknown[] = [];

beforeEach(() => {
	current = {};
	asked = [];
	auth.enterRoleView.mockClear();
	auth.exitRoleView.mockClear();
	auth.dismissNarrowedSessionEnd.mockClear();
	auth.narrowedSessionEnd = null;
	server.use(
		http.post(session("Current"), () => HttpResponse.json(current)),
		http.post(rpc("TeamService", "ListTeams"), () =>
			HttpResponse.json({ teams: [{ id: "team-stewards", orgId: "org-example", name: "Stewards" }] }),
		),
	);
});
afterEach(cleanup);

const refuse = (code: string, status: number, message: string) =>
	server.use(http.post(session("OpenTeamCast"), () => HttpResponse.json({ code, message }, { status })));

describe("act as this team", () => {
	it("asks the host for the cast and installs the session it answers", async () => {
		server.use(
			http.post(session("OpenTeamCast"), async ({ request }) => {
				asked.push(await request.json());
				return HttpResponse.json({ sessionId: "cast-session", bearer: "cast-bearer", castTeamId: "team-stewards" });
			}),
		);
		render(<ActAsTeam teamId="team-stewards" teamName="Stewards" />);
		fireEvent.click(screen.getByRole("button", { name: "Act as this team" }));
		await waitFor(() => expect(auth.enterRoleView).toHaveBeenCalledWith("cast-bearer", "team Stewards"));
		expect(asked).toEqual([{ teamId: "team-stewards" }]);
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it.each([
		["permission_denied", 403, 'team "team-stewards" holds roles you could not grant, so you may not act as it'],
		["not_found", 404, 'team "team-stewards" holds no role on anything installed at organization "org-example", so there is nothing to act as'],
		["permission_denied", 403, 'this session is already cast onto team "team-other"; a cast is never changed or widened'],
	])("shows a refusal in the host's words, with its code (%s)", async (code, status, reason) => {
		refuse(code, status, reason);
		render(<ActAsTeam teamId="team-stewards" teamName="Stewards" />);
		fireEvent.click(screen.getByRole("button", { name: "Act as this team" }));
		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toBe(`You are not acting as Stewards. The host answered: ${reason} (${code === "not_found" ? "NotFound" : "PermissionDenied"})`);
		expect(auth.enterRoleView).not.toHaveBeenCalled();
		// The way to ask again stays: a refusal is not an empty page.
		expect(screen.getByRole("button", { name: "Act as this team" })).toBeTruthy();
	});

	it("says a failure that carries no reason is one, never that the cast is open", async () => {
		server.use(http.post(session("OpenTeamCast"), () => HttpResponse.error()));
		render(<ActAsTeam teamId="team-stewards" teamName="Stewards" />);
		fireEvent.click(screen.getByRole("button", { name: "Act as this team" }));
		expect((await screen.findByRole("alert")).textContent).toContain("You are not acting as Stewards.");
		expect(auth.enterRoleView).not.toHaveBeenCalled();
	});

	it("while the host says the session is this team, says who is acting, as which team, and how to stop", async () => {
		current = { castTeamId: "team-stewards", teamIds: ["team-stewards"] };
		render(<ActAsTeam teamId="team-stewards" teamName="Stewards" />);
		const stop = await screen.findByRole("button", { name: "Stop acting as this team" });
		const section = screen.getByRole("region", { name: "Act as this team" });
		expect(section.textContent).toContain("ada@example.com");
		expect(section.textContent).toContain("acting as team Stewards");
		expect(section.textContent).toContain("attributed to the team");
		expect(screen.queryByRole("button", { name: "Act as this team" })).toBeNull();
		fireEvent.click(stop);
		expect(auth.exitRoleView).toHaveBeenCalledTimes(1);
	});

	it("on another team's page, a cast session still asks: the host refuses, the page does not decide", async () => {
		current = { castTeamId: "team-other" };
		refuse("permission_denied", 403, 'this session is already cast onto team "team-other"; a cast is never changed or widened');
		render(<ActAsTeam teamId="team-stewards" teamName="Stewards" />);
		fireEvent.click(await screen.findByRole("button", { name: "Act as this team" }));
		expect((await screen.findByRole("alert")).textContent).toContain('already cast onto team "team-other"');
	});
});

describe("the bar that says a session is narrowed", () => {
	it("says nothing for a session that is the person's own", async () => {
		const { container } = render(<RoleViewBanner />);
		await waitFor(() => expect(container.textContent).toBe(""));
	});

	it("names the person, the team by its name, the attribution, and the way out, for a cast", async () => {
		current = { castTeamId: "team-stewards", teamIds: ["team-stewards"] };
		render(<RoleViewBanner />);
		const bar = await screen.findByRole("status");
		await waitFor(() => expect(bar.textContent).toContain("Acting as team Stewards"));
		expect(bar.textContent).toContain("you are ada@example.com");
		expect(bar.textContent).toContain("nothing of your own");
		expect(bar.textContent).toContain("attributed to the team");
		fireEvent.click(screen.getByRole("button", { name: "Stop acting as this team" }));
		expect(auth.exitRoleView).toHaveBeenCalledTimes(1);
	});

	it("names the team by its id when the cast may not read the roster, rather than not at all", async () => {
		current = { castTeamId: "team-stewards" };
		server.use(
			http.post(rpc("TeamService", "ListTeams"), () =>
				HttpResponse.json({ code: "permission_denied", message: "this session acts as team team-stewards" }, { status: 403 }),
			),
		);
		render(<RoleViewBanner />);
		expect((await screen.findByRole("status")).textContent).toContain("Acting as team team-stewards");
	});

	it("says a role view opened inside a cast is both", async () => {
		current = { castTeamId: "team-stewards", roleView: [{ installationId: "inst-a", roles: ["viewer"] }] };
		render(<RoleViewBanner />);
		const bar = await screen.findByRole("status");
		expect(bar.textContent).toContain("Acting as team");
		expect(bar.textContent).toContain("Viewing as viewer on inst-a.");
	});

	it("an expired cast is said by name, and the only way on is to ask for your own session back", async () => {
		auth.narrowedSessionEnd = { reason: "expired", as: "team Stewards" };
		current = { castTeamId: "team-stewards" };
		render(<RoleViewBanner />);
		const bar = await screen.findByRole("alert");
		expect(bar.textContent).toContain("Your session acting as team Stewards has ended");
		expect(bar.textContent).toContain("you are not back in your own session until you choose to be");
		expect(screen.queryByRole("status")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Return to my own session" }));
		expect(auth.exitRoleView).toHaveBeenCalledTimes(1);
		expect(auth.dismissNarrowedSessionEnd).not.toHaveBeenCalled();
	});

	it("a cast a reload ended is said by name, with the person told they are themselves again", async () => {
		auth.narrowedSessionEnd = { reason: "reloaded", as: "viewer on records" };
		render(<RoleViewBanner />);
		const bar = await screen.findByRole("alert");
		expect(bar.textContent).toContain("Your session acting as viewer on records has ended");
		expect(bar.textContent).toContain("You are back in your own session");
		fireEvent.click(screen.getByRole("button", { name: "Understood" }));
		expect(auth.dismissNarrowedSessionEnd).toHaveBeenCalledTimes(1);
		expect(auth.exitRoleView).not.toHaveBeenCalled();
	});

	it("still says a role view is one", async () => {
		current = { roleView: [{ installationId: "inst-a", roles: ["viewer"] }] };
		render(<RoleViewBanner />);
		const bar = await screen.findByRole("status");
		expect(bar.textContent).toContain("Role view");
		expect(bar.textContent).toContain("viewer on inst-a");
		expect(screen.getByRole("button", { name: "Back to all my roles" })).toBeTruthy();
	});
});
