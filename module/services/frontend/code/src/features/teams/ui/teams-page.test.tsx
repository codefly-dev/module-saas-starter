import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

// TeamsPage reads the active tenant from the signed access token via useAuth,
// which throws outside an AuthProvider. Supply a stable org so the page can
// issue its ListTeams query. OrgSelector consumes the same mocked hook.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "org-1",
		switchOrganization: vi.fn(async () => undefined),
	}),
}));

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
