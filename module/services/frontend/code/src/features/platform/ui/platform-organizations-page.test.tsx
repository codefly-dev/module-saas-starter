import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { Profiler } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

const authState = vi.hoisted(() => ({
	platformRole: "super_admin" as string | undefined,
	user: { id: "admin-1" },
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

import { PlatformOrganizationsPage } from "./platform-organizations-page";

afterEach(() => {
	cleanup();
	authState.platformRole = "super_admin";
});

describe("PlatformOrganizationsPage", () => {
	it("lists every organization with its member count and archive state", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "ListAllOrganizations"), () =>
				HttpResponse.json({
					organizations: [
						{
							organization: { id: "org-a", name: "Acme", slug: "acme" },
							memberCount: 4,
						},
						{
							organization: {
								id: "org-z",
								name: "Zeta",
								slug: "zeta-deleted-abc",
								archivedAt: "2026-09-30T00:00:00Z",
							},
							memberCount: 0,
						},
					],
				}),
			),
		);
		renderInApp(<PlatformOrganizationsPage />);
		expect(await screen.findByRole("button", { name: "Acme" })).toBeTruthy();
		expect(screen.getByText("4")).toBeTruthy();
		expect(screen.getByText("Archived")).toBeTruthy();
		expect(
			screen.getByRole("button", { name: "Create organization" }),
		).toBeTruthy();
	});

	// The rows were a new array on every render, and TanStack Table answers a new
	// array with a page reset that re-renders the page: the first keystroke in
	// the search box started a loop that froze the tab.
	it("settles after the search box is typed in", async () => {
		let commits = 0;
		renderInApp(
			<Profiler id="organizations" onRender={() => commits++}>
				<PlatformOrganizationsPage />
			</Profiler>,
		);
		expect(await screen.findAllByRole("button", { name: /./ })).toBeTruthy();
		fireEvent.change(screen.getByLabelText("Search organizations"), {
			target: { value: "ac" },
		});
		await new Promise((resolve) => setTimeout(resolve, 600));
		const settled = commits;
		await new Promise((resolve) => setTimeout(resolve, 300));
		expect(commits - settled).toBe(0);
	});

	it("tells someone without a platform role why there is nothing to see", () => {
		authState.platformRole = undefined;
		renderInApp(<PlatformOrganizationsPage />);
		expect(screen.getByText("Platform administrator required")).toBeTruthy();
	});
});
