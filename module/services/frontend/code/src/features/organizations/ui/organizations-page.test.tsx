import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { OrganizationsPage } from "./organizations-page";

function rpc(service: string, method: string) {
	return `http://localhost:3000/saas.accounts.v1.${service}/${method}`;
}
function renderInApp(ui: React.ReactElement) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>{ui}</QueryClientProvider>,
	);
}
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
