import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { RolesPage } from "./roles-page";

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
