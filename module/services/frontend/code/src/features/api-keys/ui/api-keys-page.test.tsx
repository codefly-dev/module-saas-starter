import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/setup";

// The page reads the active tenant from the signed session. Pin it so the
// container renders the table (not the "select an organization" empty state).
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "org-1",
		switchOrganization: async () => {},
	}),
}));

import { APIKeysPage } from "./api-keys-page";

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

describe("APIKeysPage admin container", () => {
	it("renders the API keys the service returns for the active org", async () => {
		server.use(
			http.post(rpc("APIKeyService", "ListAPIKeys"), () =>
				HttpResponse.json({
					keys: [
						{
							id: "key-1",
							organizationId: "org-1",
							userId: "user-1",
							name: "Production Backend",
							prefix: "sk_live_abcd",
							scopes: [{ resource: "*", action: "read" }],
							environment: 1,
							createdAt: "2026-01-02T03:04:05Z",
						},
					],
				}),
			),
		);
		renderInApp(<APIKeysPage />);
		expect(await screen.findByText("Production Backend")).toBeTruthy();
	});
});
