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

	// The table resets its page index whenever its data changes identity, and
	// that reset re-renders the page. Rows rebuilt on every render therefore
	// re-rendered the page forever; with an organization open, each render cost
	// enough to freeze the browser tab.
	it("settles once the list and an open organization have loaded", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "ListAllOrganizations"), () =>
				HttpResponse.json({
					organizations: [
						{
							organization: { id: "org-a", name: "Acme", slug: "acme" },
							memberCount: 1,
						},
					],
				}),
			),
			http.post(rpc("PlatformAdminService", "GetOrganizationRoster"), () =>
				HttpResponse.json({
					members: [
						{ orgId: "org-a", userId: "u-1", userEmail: "a@acme.test" },
					],
				}),
			),
		);
		let commits = 0;
		const countCommits = () => {
			commits++;
		};
		const idle = async () => {
			await new Promise((resolve) => setTimeout(resolve, 100));
			const before = commits;
			await new Promise((resolve) => setTimeout(resolve, 200));
			return commits - before;
		};
		renderInApp(
			<Profiler id="page" onRender={countCommits}>
				<PlatformOrganizationsPage />
			</Profiler>,
		);
		fireEvent.click(await screen.findByRole("button", { name: "Acme" }));
		expect(await screen.findByText("a@acme.test")).toBeTruthy();
		// The first window absorbs the panel's own late reads; a page that has
		// settled commits nothing in the next.
		await idle();
		expect(await idle()).toBe(0);
	});

	it("tells someone without a platform role why there is nothing to see", () => {
		authState.platformRole = undefined;
		renderInApp(<PlatformOrganizationsPage />);
		expect(screen.getByText("Platform administrator required")).toBeTruthy();
	});
});
