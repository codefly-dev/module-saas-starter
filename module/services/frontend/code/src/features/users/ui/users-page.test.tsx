import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { UsersPage } from "./users-page";

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

describe("UsersPage admin container", () => {
	it("renders the users the platform admin service returns", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "SearchUsers"), () =>
				HttpResponse.json({
					users: [
						{
							uuid: "user-1",
							primaryEmail: "admin@acme.test",
							status: 1,
							emailVerified: true,
							profile: {},
						},
					],
				}),
			),
		);
		renderInApp(<UsersPage />);
		// Email shows in the Email column and again as the Name fallback (empty
		// profile), so match all occurrences rather than a single node.
		expect(
			(await screen.findAllByText("admin@acme.test")).length,
		).toBeGreaterThan(0);
	});
});
