import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/setup";
import { SSOAdminPage } from "./sso-admin-page";

// SSOAdminPage reads the active org from useAuth (throws outside an
// AuthProvider). Stub it to a fixed org so the status query fires.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "org-1",
		switchOrganization: async () => {},
	}),
}));

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

describe("SSOAdminPage admin container", () => {
	it("renders the active SSO connection the service returns", async () => {
		server.use(
			http.post(rpc("SSOAdminService", "GetSSO"), () =>
				HttpResponse.json({
					provider: "okta",
					status: "active",
					connectionId: "conn_test123",
				}),
			),
		);
		renderInApp(<SSOAdminPage />);
		expect(
			await screen.findByText("SSO is live for this organization"),
		).toBeTruthy();
	});
});
