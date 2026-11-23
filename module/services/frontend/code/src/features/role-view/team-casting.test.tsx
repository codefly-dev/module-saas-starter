import { CastBar, openTeamSwitcher, TeamSwitcher } from "@codefly-dev/saas-ui/casting";
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import { server } from "@/test/setup";
import { HostCasting } from "./team-casting";

// This host's wiring of the kit's team casting: the kit's controls read the
// session and the teams through this host's own session interface, and a
// cast the host opens is adopted as the tab's narrowed session.
const auth = vi.hoisted(() => ({
	isAuthenticated: true,
	user: { id: "user-example" },
	organizationId: "org-example",
	enterRoleView: vi.fn(),
	exitRoleView: vi.fn(() => Promise.resolve()),
	// The provider's one read of the host's answer: here, the host asked each time.
	hostSession: vi.fn(
		async (_again?: boolean): Promise<Record<string, unknown>> => ({}),
	),
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => auth }));
const router = vi.hoisted(() => ({ push: vi.fn(), path: "/admin/teams" }));
vi.mock("next/navigation", () => ({
	useRouter: () => router,
	usePathname: () => router.path,
}));

const session = (method: string) =>
	`http://localhost:3000/host.session.v1.SessionService/${method}`;
const own = {
	personId: "user-example",
	personDisplayName: "Ada",
	organizationId: "org-example",
};
let cast = "";
let askedOfTheHost = 0;

beforeEach(() => {
	cast = "";
	askedOfTheHost = 0;
	auth.hostSession.mockReset();
	auth.hostSession.mockImplementation(async () => ({
		...own,
		castTeamId: cast,
		roleView: [],
	}));
	sessionStorage.clear();
	auth.enterRoleView.mockReset();
	auth.enterRoleView.mockImplementation((bearer: string) => {
		cast = bearer.replace("bearer-", "");
	});
	auth.exitRoleView.mockClear();
	router.push.mockReset();
	server.use(
		http.get("http://localhost:3000/api/solutions", () =>
			HttpResponse.json({ solutions: [{ id: "records", nav: { title: "Records" } }] }),
		),
		http.post(session("Current"), () => {
			askedOfTheHost += 1;
			return HttpResponse.json(cast ? { ...own, castTeamId: cast } : own);
		}),
		http.post(session("ListCastableTeams"), () =>
			HttpResponse.json({
				teams: [
					{
						teamId: "team-stewards",
						displayName: "Stewards",
						holds: [{ installationId: "inst-a", target: "records", role: "steward" }],
					},
				].filter((t) => !cast || t.teamId === cast),
			}),
		),
		http.post(session("OpenTeamCast"), async ({ request }) => {
			const { teamId } = (await request.json()) as { teamId: string };
			return HttpResponse.json({ ...own, castTeamId: teamId, bearer: `bearer-${teamId}` });
		}),
	);
});
afterEach(cleanup);

describe("this host's team casting", () => {
	it("adopts the cast the host opens as the tab's session, and leaves Admin for where the team starts", async () => {
		renderInApp(
			<HostCasting>
				<CastBar />
				<TeamSwitcher />
			</HostCasting>,
		);
		act(() => openTeamSwitcher());
		fireEvent.click(await screen.findByText("Stewards"));
		await waitFor(() =>
			expect(auth.enterRoleView).toHaveBeenCalledWith("bearer-team-stewards", "team Stewards"),
		);
		expect(router.push).toHaveBeenCalledWith("/");
		await waitFor(() =>
			expect(screen.getByRole("status").textContent).toContain("Viewing as Stewards"),
		);
		fireEvent.click(screen.getByRole("button", { name: /Back to my view/ }));
		await waitFor(() => expect(auth.exitRoleView).toHaveBeenCalledTimes(1));
	});

	// The auth provider reads the session for every installed token. The kit's
	// read here is that answer, never a second ask of the host for the same
	// thing; once it has been given, a later read asks for it anew.
	it("reads the session the provider already read, and asks anew only afterwards", async () => {
		renderInApp(
			<HostCasting>
				<CastBar />
				<TeamSwitcher />
			</HostCasting>,
		);
		act(() => openTeamSwitcher());
		await screen.findByText("Stewards");
		expect(auth.hostSession.mock.calls[0]).toEqual([false]);

		fireEvent.click(screen.getByText("Stewards"));
		await waitFor(() =>
			expect(screen.getByRole("status").textContent).toContain("Viewing as Stewards"),
		);
		expect(auth.hostSession.mock.calls.at(-1)).toEqual([true]);
		expect(askedOfTheHost).toBe(0);
	});
});
