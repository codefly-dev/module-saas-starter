import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { OrganizationsPage } from "./organizations-page";

afterEach(cleanup);

describe("OrganizationsPage admin container", () => {
	it("renders the organizations the service returns", async () => {
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
		expect(await screen.findByText("Acme Corporation")).toBeTruthy();
	});
});
