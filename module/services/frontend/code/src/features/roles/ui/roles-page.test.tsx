import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { RolesPage } from "./roles-page";

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

describe("RolesPage admin container", () => {
	it("renders the roles the permission service returns", async () => {
		server.use(
			http.post(rpc("PermissionService", "ListRoles"), () =>
				HttpResponse.json({
					roles: [
						{
							id: "role-1",
							name: "Editor",
							description: "Can edit content",
							permissions: [{ resource: "content", action: "write" }],
							builtIn: false,
						},
					],
				}),
			),
		);

		renderInApp(<RolesPage />);

		expect(await screen.findByText("Editor")).toBeTruthy();
		expect(screen.getByText("content:write")).toBeTruthy();
	});

	it("shows the empty state when no roles are defined", async () => {
		renderInApp(<RolesPage />);

		expect(await screen.findByText("No roles defined")).toBeTruthy();
	});
});
