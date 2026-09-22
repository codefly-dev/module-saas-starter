import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { OrgSettingsPage } from "./org-settings-page";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-example" }),
}));
afterEach(cleanup);

describe("organization settings tenant selection", () => {
	it("loads settings for the selected organization, never a default placeholder", async () => {
		let body: unknown;
		server.use(
			http.post(
				rpc("OrganizationService", "GetOrgSettings"),
				async ({ request }) => {
					body = await request.json();
					return HttpResponse.json({
						orgId: "org-example",
						customDomain: "app.example.com",
					});
				},
			),
		);
		renderInApp(<OrgSettingsPage />);
		expect(await screen.findByDisplayValue("app.example.com")).toBeTruthy();
		expect(body).toEqual({ orgId: "org-example" });
	});
	it("offers retry instead of an editable blank form on load failure", async () => {
		server.use(
			http.post(rpc("OrganizationService", "GetOrgSettings"), () =>
				HttpResponse.json(
					{ code: "unavailable", message: "Unavailable" },
					{ status: 503 },
				),
			),
		);
		renderInApp(<OrgSettingsPage />);
		expect((await screen.findByRole("alert")).textContent).toContain(
			"Couldn't load organization settings",
		);
		expect(screen.queryByRole("button", { name: "Save Settings" })).toBeNull();
	});
});
