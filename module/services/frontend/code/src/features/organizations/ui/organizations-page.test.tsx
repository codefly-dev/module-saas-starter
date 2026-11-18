import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { OrganizationsPage } from "./organizations-page";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ isAuthenticated: true, user: { id: "owner-1" } }),
}));

afterEach(cleanup);

describe("OrganizationsPage admin container", () => {
	it("opens an organization directly from its name", async () => {
		server.use(
			http.post(rpc("OrganizationService", "ListOrganizations"), () =>
				HttpResponse.json({
					organizations: [
						{
							id: "org-1",
							name: "Acme Corporation",
							slug: "acme",
							ownerId: "owner-1",
						},
					],
				}),
			),
		);
		renderInApp(<OrganizationsPage />);
		fireEvent.click(await screen.findByRole("button", { name: "Manage Acme Corporation" }));
		expect(await screen.findByRole("heading", { name: "Members of Acme Corporation" })).toBeTruthy();
	});
});
