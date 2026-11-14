import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import { server } from "@/test/setup";
import { ActAsTeam, RoleViewBanner } from "./role-view";
import { HostCasting } from "./team-casting";

// The person on the page holds no role the browser knows of. Nothing here may
// decide who can cast: that is the authority's, and these tests would pass a
// client-side gate only by lying about a role.
const auth = vi.hoisted(() => ({
	isAuthenticated: true,
	accessToken: "own-session",
	organizationId: "org-example",
	orgRole: undefined as string | undefined,
	platformRole: undefined as string | undefined,
	user: { id: "user-example", email: "ada@example.com" },
	enterRoleView: vi.fn(),
	exitRoleView: vi.fn(() => Promise.resolve()),
	sessionRead: "confirmed" as "reading" | "failed" | "confirmed",
	narrowedSessionEnd: null as {
		reason: "expired" | "reloaded";
		as: string;
	} | null,
	narrowedSessionEnding: null as { as: string; endsAt: number } | null,
	dismissNarrowedSessionEnd: vi.fn(),
}));
const router = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({
	useRouter: () => router,
	usePathname: () => "/admin/teams/team-stewards",
}));
// The cast is the provider's reading of the host's Session.cast_team_id; the
// card reads it from the same place the menus do.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		...auth,
		castTeamId: (current.castTeamId as string) ?? "",
		roleView:
			(current.roleView as { installationId: string; roles: string[] }[]) ?? [],
	}),
}));

const session = (method: string) =>
	`http://localhost:3000/host.session.v1.SessionService/${method}`;
let current: Record<string, unknown> = {};
let asked: string[] = [];

beforeEach(() => {
	current = {};
	asked = [];
	auth.enterRoleView.mockClear();
	auth.exitRoleView.mockClear();
	auth.exitRoleView.mockImplementation(() => {
		asked.push("exit");
		return Promise.resolve();
	});
	auth.dismissNarrowedSessionEnd.mockClear();
	router.push.mockClear();
	auth.narrowedSessionEnd = null;
	auth.narrowedSessionEnding = null;
	auth.sessionRead = "confirmed";
	auth.orgRole = undefined;
	sessionStorage.clear();
	server.use(
		http.get("http://localhost:3000/api/solutions", () =>
			HttpResponse.json({
				solutions: [{ id: "records", nav: { title: "Records" } }],
			}),
		),
		// The host's answers: who the session is, and the teams it lists.
		http.post(session("Current"), () =>
			HttpResponse.json({
				personId: "user-example",
				organizationId: "org-example",
				...current,
			}),
		),
		http.post(session("ListCastableTeams"), () =>
			HttpResponse.json({
				teams: [
					{
						teamId: "team-stewards",
						displayName: "Stewards",
						holds: [
							{ installationId: "inst-a", target: "records", role: "steward" },
						],
					},
				],
			}),
		),
	);
});
afterEach(cleanup);

const refuse = (code: string, status: number, message: string) =>
	server.use(
		http.post(session("OpenTeamCast"), () =>
			HttpResponse.json({ code, message }, { status }),
		),
	);
const viewAs = async () =>
	fireEvent.click(
		await screen.findByRole("button", { name: "View as this team" }),
	);
// The card as the page mounts it: under this host's casting, which reads the
// session and the teams from the host.
const card = (key = "page") => (
	<HostCasting key={key}>
		<ActAsTeam teamId="team-stewards" teamName="Stewards" />
	</HostCasting>
);

describe("view as this team", () => {
	it("asks the host for the cast, installs the session it answers, and lands where the team starts", async () => {
		server.use(
			http.post(session("OpenTeamCast"), async ({ request }) => {
				asked.push(JSON.stringify(await request.json()));
				return HttpResponse.json({
					sessionId: "cast-session",
					bearer: "cast-bearer",
					castTeamId: "team-stewards",
				});
			}),
		);
		renderInApp(card());
		await viewAs();
		await waitFor(() =>
			expect(auth.enterRoleView).toHaveBeenCalledWith(
				"cast-bearer",
				"team Stewards",
			),
		);
		expect(asked).toEqual(['{"teamId":"team-stewards"}']);
		expect(router.push).toHaveBeenCalledWith("/");
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it.each([
		[
			"permission_denied",
			403,
			'team "team-stewards" holds roles you could not grant, so you may not act as it',
		],
		[
			"not_found",
			404,
			'team "team-stewards" holds no role on anything installed at organization "org-example", so there is nothing to act as',
		],
	])(
		"shows a refusal in the host's words, with its code (%s)",
		async (code, status, reason) => {
			refuse(code, status, reason);
			renderInApp(card());
			await viewAs();
			const alert = await screen.findByRole("alert");
			expect(alert.textContent).toBe(
				`Not viewing as Stewards: ${reason} (${code === "not_found" ? "NotFound" : "PermissionDenied"})`,
			);
			expect(auth.enterRoleView).not.toHaveBeenCalled();
			// The way to ask again stays: a refusal is not an empty page.
			expect(
				screen.getByRole("button", { name: "View as this team" }),
			).toBeTruthy();
		},
	);

	it("says a failure that carries no reason is one, never that the cast is open", async () => {
		server.use(http.post(session("OpenTeamCast"), () => HttpResponse.error()));
		renderInApp(card());
		await viewAs();
		expect((await screen.findByRole("alert")).textContent).toContain(
			"Not viewing as Stewards:",
		);
		expect(auth.enterRoleView).not.toHaveBeenCalled();
	});

	it("while the host says the session is this team, says so and offers the way back", async () => {
		current = { castTeamId: "team-stewards" };
		renderInApp(card());
		const back = await screen.findByRole("button", { name: /Back to my view/ });
		const section = screen.getByRole("region", { name: "View as this team" });
		expect(section.textContent).toContain("You are viewing as this team");
		expect(section.textContent).toContain("attributed to the team");
		expect(
			screen.queryByRole("button", { name: "View as this team" }),
		).toBeNull();
		fireEvent.click(back);
		await waitFor(() => expect(auth.exitRoleView).toHaveBeenCalledTimes(1));
	});

	it("on another team's page, a cast goes back to the person's own session and asks from there: the host decides", async () => {
		current = { castTeamId: "team-other" };
		server.use(
			http.post(session("OpenTeamCast"), () => {
				asked.push("open");
				return HttpResponse.json({ bearer: "cast-bearer" });
			}),
		);
		renderInApp(card());
		await viewAs();
		await waitFor(() =>
			expect(auth.enterRoleView).toHaveBeenCalledWith(
				"cast-bearer",
				"team Stewards",
			),
		);
		expect(asked).toEqual(["exit", "open"]);
	});

	it("on another team's page, a refusal after the way back says the person is in their own view, and outlives the remount that view causes", async () => {
		current = { castTeamId: "team-other" };
		refuse("permission_denied", 403, "holds roles you could not grant");
		const { rerender } = renderInApp(card("cast"));
		await viewAs();
		const said =
			"You are back in your own view. Stewards was not opened: holds roles you could not grant (PermissionDenied)";
		expect((await screen.findByRole("alert")).textContent).toBe(said);
		// The person's own session is confirmed, and the authority boundary
		// (lib/providers.tsx) remounts the page under it.
		current = {};
		rerender(card("own"));
		expect((await screen.findByRole("alert")).textContent).toBe(said);
	});

	it("shows what the team holds before anyone asks, as the host lists it", async () => {
		renderInApp(card());
		expect(await screen.findByText("records · steward")).toBeTruthy();
	});
});

describe("the notice that a narrowed session ended or is unconfirmed", () => {
	it("says nothing for a session that is the person's own, nor for a live cast: the bar in the page says that", async () => {
		const { container, rerender } = renderInApp(<RoleViewBanner />);
		expect(container.textContent).toBe("");
		current = { castTeamId: "team-stewards" };
		rerender(<RoleViewBanner />);
		expect(container.textContent).toBe("");
	});

	it("an expired cast is said by name, and the only way on is to ask for your own session back", async () => {
		auth.narrowedSessionEnd = { reason: "expired", as: "team Stewards" };
		current = { castTeamId: "team-stewards" };
		renderInApp(<RoleViewBanner />);
		const bar = await screen.findByRole("alert");
		expect(bar.textContent).toContain(
			"Your session acting as team Stewards has ended",
		);
		expect(bar.textContent).toContain(
			"you are not back in your own session until you choose to be",
		);
		expect(screen.queryByRole("status")).toBeNull();
		fireEvent.click(
			screen.getByRole("button", { name: "Return to my own session" }),
		);
		expect(auth.exitRoleView).toHaveBeenCalledTimes(1);
		expect(auth.dismissNarrowedSessionEnd).not.toHaveBeenCalled();
	});

	it("a cast the host refused to reopen is said before it ends, with when, and the way back to your own session", async () => {
		const endsAt = new Date(2026, 10, 14, 15, 42).getTime();
		auth.narrowedSessionEnding = { as: "team Stewards", endsAt };
		current = { castTeamId: "team-stewards" };
		renderInApp(<RoleViewBanner />);
		const bar = await screen.findByRole("status");
		const at = new Date(endsAt).toLocaleTimeString([], {
			hour: "2-digit",
			minute: "2-digit",
		});
		expect(bar.textContent).toContain(
			`Your session acting as team Stewards will end at ${at}`,
		);
		expect(bar.textContent).toContain("it could not be renewed");
		fireEvent.click(
			screen.getByRole("button", { name: "Return to my own session" }),
		);
		expect(auth.exitRoleView).toHaveBeenCalledTimes(1);
	});

	it("an end already said outranks the notice that it would end", async () => {
		auth.narrowedSessionEnd = { reason: "expired", as: "team Stewards" };
		auth.narrowedSessionEnding = { as: "team Stewards", endsAt: Date.now() };
		renderInApp(<RoleViewBanner />);
		expect((await screen.findByRole("alert")).textContent).toContain(
			"has ended",
		);
		expect(screen.queryByRole("status")).toBeNull();
	});

	it("a cast a reload ended is said by name, with the person told they are themselves again", async () => {
		auth.narrowedSessionEnd = { reason: "reloaded", as: "viewer on records" };
		renderInApp(<RoleViewBanner />);
		const bar = await screen.findByRole("alert");
		expect(bar.textContent).toContain(
			"Your session acting as viewer on records has ended",
		);
		expect(bar.textContent).toContain("You are back in your own session");
		fireEvent.click(screen.getByRole("button", { name: "Understood" }));
		expect(auth.dismissNarrowedSessionEnd).toHaveBeenCalledTimes(1);
		expect(auth.exitRoleView).not.toHaveBeenCalled();
	});

	it.each([
		["reading", "Confirming what this session is"],
		["failed", "Could not confirm what this session is"],
	] as const)(
		"says a session the host has not confirmed is %s, never 'not a cast'",
		(state, words) => {
			auth.sessionRead = state;
			renderInApp(<RoleViewBanner />);
			const bar = screen.getByRole("status");
			expect(bar.textContent).toContain(words);
			expect(bar.textContent).toContain("no administrator access is offered");
		},
	);
});
