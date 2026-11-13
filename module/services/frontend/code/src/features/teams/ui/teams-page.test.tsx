import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

vi.mock("next/navigation", () => ({
	useRouter: () => ({ push: vi.fn() }),
	usePathname: () => "/admin/teams",
}));

// TeamsPage reads the active tenant from the signed access token via useAuth,
// which throws outside an AuthProvider. Supply a stable org so the page can
// issue its ListTeams query. OrgSelector consumes the same mocked hook.
const auth = vi.hoisted(() => ({
	isAuthenticated: true,
	user: { id: "user-1" },
	organizationId: "org-1",
	orgRole: undefined as string | undefined,
	sessionRead: "confirmed",
	castTeamId: "",
	roleView: [],
	enterRoleView: vi.fn(),
	exitRoleView: vi.fn(async () => undefined),
	switchOrganization: vi.fn(async () => undefined),
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => auth }));

import { HostCasting } from "@/features/role-view/team-casting";
import { TeamsPage } from "./teams-page";

afterEach(cleanup);

describe("TeamsPage admin container", () => {
	it("renders the teams the team service returns for the active org", async () => {
		server.use(
			http.post(rpc("TeamService", "ListTeams"), () =>
				HttpResponse.json({
					teams: [
						{
							id: "team-1",
							orgId: "org-1",
							name: "Platform Engineering",
							description: "Owns the shared platform",
						},
					],
				}),
			),
		);
		renderInApp(<TeamsPage />);
		expect(await screen.findByText("Platform Engineering")).toBeTruthy();
	});
});

describe("viewing as a team from the list", () => {
	it("says what each team holds and views as one from its row; the host decides", async () => {
		auth.orgRole = "admin";
		const session = "http://localhost:3000/host.session.v1.SessionService";
		let asked = "";
		server.use(
			http.get("http://localhost:3000/api/solutions", () =>
				HttpResponse.json({ solutions: [] }),
			),
			http.post(rpc("TeamService", "ListTeams"), () =>
				HttpResponse.json({
					teams: [
						{ id: "team-1", orgId: "org-1", name: "Readers" },
						{ id: "team-2", orgId: "org-1", name: "Finance" },
					],
				}),
			),
			// The host lists the teams this person may act as, with what each holds.
			http.post(`${session}/Current`, () =>
				HttpResponse.json({ personId: "user-1", organizationId: "org-1" }),
			),
			http.post(`${session}/ListCastableTeams`, () =>
				HttpResponse.json({
					teams: [
						{
							teamId: "team-1",
							displayName: "Readers",
							holds: [{ installationId: "inst-a", target: "records", role: "viewer" }],
						},
					],
				}),
			),
			http.post(`${session}/OpenTeamCast`, async ({ request }) => {
				asked = JSON.stringify(await request.json());
				return HttpResponse.json({ bearer: "cast-bearer" });
			}),
		);
		renderInApp(
			<HostCasting>
				<TeamsPage />
			</HostCasting>,
		);
		expect(await screen.findByText("records · viewer")).toBeTruthy();
		expect(screen.getByText("Holds nothing installed")).toBeTruthy();
		// A team granted nothing has nothing to view as.
		expect(
			screen.queryByRole("button", { name: "View as Finance" }),
		).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "View as Readers" }));
		await waitFor(() =>
			expect(auth.enterRoleView).toHaveBeenCalledWith(
				"cast-bearer",
				"team Readers",
			),
		);
		expect(asked).toBe('{"teamId":"team-1"}');
	});
});
