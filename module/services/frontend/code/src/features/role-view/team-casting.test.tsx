import {
	act,
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
import {
	CastIndicator,
	setTeamSwitcherOpen,
	TeamSwitcher,
} from "./team-casting";

// An administrator in the browser's eyes, so the page reads the organization's
// teams and what they hold. Who may act as which team is still the host's:
// every cast here is asked of it, and its refusal is what the page shows.
const auth = vi.hoisted(() => ({
	isAuthenticated: true,
	accessToken: "own-session",
	organizationId: "org-example",
	orgRole: "admin" as string | undefined,
	platformRole: undefined as string | undefined,
	user: { id: "user-example", email: "ada@example.com" },
	sessionRead: "confirmed" as "reading" | "failed" | "confirmed",
	castTeamId: "",
	roleView: [] as { installationId: string; roles: string[] }[],
	enterRoleView: vi.fn(),
	exitRoleView: vi.fn(() => Promise.resolve()),
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => auth }));
const router = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({
	useRouter: () => router,
	usePathname: () => "/",
}));

const session = (method: string) =>
	`http://localhost:3000/host.session.v1.SessionService/${method}`;
const host = (method: string) =>
	`http://localhost:3000/host.installation.v1.InstallationService/${method}`;
const teams = [
	{ id: "team-stewards", orgId: "org-example", name: "Stewards" },
	{ id: "team-readers", orgId: "org-example", name: "Readers" },
	{ id: "team-empty", orgId: "org-example", name: "Nobody" },
];
let steps: string[] = [];

beforeEach(() => {
	steps = [];
	sessionStorage.clear();
	Object.assign(auth, {
		castTeamId: "",
		roleView: [],
		orgRole: "admin",
		sessionRead: "confirmed",
	});
	auth.enterRoleView.mockReset();
	auth.exitRoleView.mockReset();
	auth.exitRoleView.mockImplementation(() => {
		steps.push("exit");
		return Promise.resolve();
	});
	server.use(
		http.get("http://localhost:3000/api/solutions", () =>
			HttpResponse.json({ solutions: [] }),
		),
		http.post(rpc("TeamService", "ListTeams"), () =>
			HttpResponse.json({ teams }),
		),
		http.post(host("List"), () =>
			HttpResponse.json({
				installations: [
					{ id: "inst-a", target: "records", status: "STATUS_ACTIVE" },
				],
			}),
		),
		http.post(host("ListRoleGrants"), () =>
			HttpResponse.json({
				grants: [
					{ role: "steward", teamId: "team-stewards" },
					{ role: "viewer", teamId: "team-readers" },
				],
			}),
		),
		http.post(session("OpenTeamCast"), async ({ request }) => {
			const { teamId } = (await request.json()) as { teamId: string };
			steps.push(`open ${teamId}`);
			return HttpResponse.json({ bearer: `bearer-${teamId}` });
		}),
	);
});
afterEach(() => {
	act(() => setTeamSwitcherOpen(false));
	cleanup();
});

const openSwitcher = () => act(() => setTeamSwitcherOpen(true));
const keep = (kept: unknown) =>
	sessionStorage.setItem(
		"codefly_cast_directory",
		JSON.stringify({ orgId: "org-example", teams: kept }),
	);
const kept = [
	{
		id: "team-stewards",
		name: "Stewards",
		holds: [
			{
				installationId: "inst-a",
				target: "records",
				title: "records",
				role: "steward",
			},
		],
	},
	{
		id: "team-readers",
		name: "Readers",
		holds: [
			{
				installationId: "inst-a",
				target: "records",
				title: "records",
				role: "viewer",
			},
		],
	},
];

describe("the team switcher", () => {
	it("lists the teams that hold something, grouped by what they hold on, each with its role, and casts on a choice", async () => {
		renderInApp(<TeamSwitcher />);
		openSwitcher();
		const stewards = await screen.findByRole("option", { name: /Stewards/ });
		expect(stewards.textContent).toContain("records · steward");
		expect(
			screen.getByRole("option", { name: /Readers/ }).textContent,
		).toContain("records · viewer");
		expect(screen.getByText("records")).toBeTruthy();
		// A team granted nothing has nothing to act as: the host would answer not found.
		expect(screen.queryByRole("option", { name: /Nobody/ })).toBeNull();
		fireEvent.click(screen.getByRole("option", { name: /Readers/ }));
		await waitFor(() =>
			expect(auth.enterRoleView).toHaveBeenCalledWith(
				"bearer-team-readers",
				"team Readers",
			),
		);
		expect(steps).toEqual(["open team-readers"]);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("narrows the list to what the search names: a team, a page or a role", async () => {
		renderInApp(<TeamSwitcher />);
		openSwitcher();
		await screen.findByRole("option", { name: /Stewards/ });
		fireEvent.change(screen.getByRole("combobox", { name: "Search teams" }), {
			target: { value: "viewer" },
		});
		await waitFor(() =>
			expect(screen.queryByRole("option", { name: /Stewards/ })).toBeNull(),
		);
		expect(screen.getByRole("option", { name: /Readers/ })).toBeTruthy();
	});

	it("while cast, lists what this tab read before the cast, marks the current team, and switches in one choice: back to the person, then the new cast", async () => {
		auth.castTeamId = "team-stewards";
		auth.orgRole = undefined;
		keep(kept);
		renderInApp(<TeamSwitcher />);
		openSwitcher();
		const current = await screen.findByRole("option", { name: /Stewards/ });
		expect(within(current).getByLabelText("Current")).toBeTruthy();
		fireEvent.click(screen.getByRole("option", { name: /Readers/ }));
		await waitFor(() =>
			expect(auth.enterRoleView).toHaveBeenCalledWith(
				"bearer-team-readers",
				"team Readers",
			),
		);
		expect(steps).toEqual(["exit", "open team-readers"]);
	});

	it("a switch the host refuses leaves the person in their own view, and says so with the host's reason", async () => {
		auth.castTeamId = "team-stewards";
		keep(kept);
		server.use(
			http.post(session("OpenTeamCast"), () =>
				HttpResponse.json(
					{
						code: "permission_denied",
						message: "holds roles you could not grant",
					},
					{ status: 403 },
				),
			),
		);
		renderInApp(<TeamSwitcher />);
		openSwitcher();
		fireEvent.click(await screen.findByRole("option", { name: /Readers/ }));
		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toBe(
			"You are back in your own view. The host did not open Readers: holds roles you could not grant (PermissionDenied)",
		);
		expect(steps).toEqual(["exit"]);
		expect(auth.enterRoleView).not.toHaveBeenCalled();
	});

	it("a switch whose way back fails says so, and opens nothing: the person is still in the cast", async () => {
		auth.castTeamId = "team-stewards";
		keep(kept);
		auth.exitRoleView.mockImplementation(() =>
			Promise.reject(
				new Error("Could not restore your session — please try again."),
			),
		);
		renderInApp(<TeamSwitcher />);
		openSwitcher();
		fireEvent.click(await screen.findByRole("option", { name: /Readers/ }));
		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain(
			"Not viewing as Readers: Could not restore your session",
		);
		expect(alert.textContent).not.toContain("back in your own view");
		expect(steps).toEqual([]);
		expect(auth.enterRoleView).not.toHaveBeenCalled();
	});

	it("offers the way back while cast", async () => {
		auth.castTeamId = "team-stewards";
		keep(kept);
		renderInApp(<TeamSwitcher />);
		openSwitcher();
		fireEvent.click(
			await screen.findByRole("button", { name: "Back to my view" }),
		);
		await waitFor(() => expect(auth.exitRoleView).toHaveBeenCalledTimes(1));
	});
});

describe("the cast bar", () => {
	it("says nothing for the person's own session", () => {
		const { container } = renderInApp(<CastIndicator />);
		expect(container.textContent).toBe("");
	});

	it("names the team and what the host says the cast holds, in one line, with the way to switch and the way back", async () => {
		auth.castTeamId = "team-stewards";
		keep(kept);
		server.use(
			http.post(host("ListHeld"), () =>
				HttpResponse.json({
					installations: [
						{
							installation: { id: "inst-a", target: "records" },
							roles: ["steward", "editor"],
						},
					],
				}),
			),
		);
		renderInApp(
			<>
				<CastIndicator />
				<TeamSwitcher />
			</>,
		);
		const bar = screen.getByRole("status", {
			name: "Viewing as team Stewards",
		});
		await waitFor(() =>
			expect(bar.textContent).toContain("records · steward, records · editor"),
		);
		fireEvent.click(within(bar).getByRole("button", { name: "Switch team" }));
		expect(await screen.findByRole("dialog")).toBeTruthy();
		fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		fireEvent.click(
			within(bar).getByRole("button", { name: "Back to my view" }),
		);
		await waitFor(() => expect(auth.exitRoleView).toHaveBeenCalledTimes(1));
	});

	it("says who is acting, the attribution and the personal sources in its details", async () => {
		auth.castTeamId = "team-stewards";
		keep(kept);
		renderInApp(<CastIndicator />);
		fireEvent.click(screen.getByRole("button", { name: "What this view is" }));
		const details = await screen.findByText(/viewing as team/);
		expect(details.textContent).toContain("ada@example.com");
		expect(screen.getByText(/attributed to the team/).textContent).toContain(
			"still read as you",
		);
	});

	it("names the team by its id when nothing names it, rather than not at all", () => {
		auth.castTeamId = "team-stewards";
		server.use(
			http.post(rpc("TeamService", "ListTeams"), () =>
				HttpResponse.json(
					{ code: "permission_denied", message: "a cast reads no roster" },
					{ status: 403 },
				),
			),
		);
		renderInApp(<CastIndicator />);
		expect(
			screen.getByRole("status", { name: "Viewing as team team-stewards" }),
		).toBeTruthy();
	});

	it("still says a role view is one, with the way back to all of the person's roles", () => {
		auth.roleView = [{ installationId: "inst-a", roles: ["viewer"] }];
		renderInApp(<CastIndicator />);
		const bar = screen.getByRole("status", { name: "Role view" });
		expect(bar.textContent).toContain("viewer on inst-a");
		expect(
			within(bar).queryByRole("button", { name: "Switch team" }),
		).toBeNull();
		fireEvent.click(
			within(bar).getByRole("button", { name: "Back to all my roles" }),
		);
		expect(auth.exitRoleView).toHaveBeenCalledTimes(1);
	});
});
