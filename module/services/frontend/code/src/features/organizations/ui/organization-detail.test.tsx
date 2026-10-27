import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { Profiler } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

const authState = vi.hoisted(() => ({
	platformRole: "super_admin" as string | undefined,
	orgRole: undefined,
	isAuthenticated: true,
	user: { id: "admin-1" },
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

import { OrganizationDetail } from "./organization-detail";

afterEach(cleanup);

const settle = () => new Promise((resolve) => setTimeout(resolve, 300));

describe("OrganizationDetail", () => {
	// Picking a directory user re-renders the members panel. Its table rows were
	// a new array on every render, which TanStack Table answers with a page
	// reset — a state update on the panel — so the panel re-rendered forever. In
	// a browser that loop ran at the click's synchronous priority and froze the
	// tab; here it shows as commits that never stop.
	it("settles after a platform administrator picks a user to add", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "GetOrganizationRoster"), () =>
				HttpResponse.json({
					members: [
						{
							orgId: "org-acme",
							userId: "user-owner",
							userEmail: "owner@example.com",
							role: "ORG_ROLE_OWNER",
						},
					],
				}),
			),
			http.post(rpc("PlatformAdminService", "SearchUsers"), () =>
				HttpResponse.json({
					users: [
						{
							uuid: "user-jane",
							primaryEmail: "jane@example.com",
							status: "USER_STATUS_ACTIVE",
						},
					],
					nextPageToken: "",
				}),
			),
			http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
				HttpResponse.json({ assignments: [] }),
			),
		);
		let commits = 0;
		renderInApp(
			<Profiler id="organization" onRender={() => commits++}>
				<OrganizationDetail
					organization={{ id: "org-acme", name: "Acme", slug: "acme" }}
					source="platform"
				/>
			</Profiler>,
		);
		expect(await screen.findByText("owner@example.com")).toBeTruthy();
		fireEvent.change(screen.getByPlaceholderText("Search by name or email…"), {
			target: { value: "jane" },
		});
		fireEvent.click(
			await screen.findByRole("button", { name: "jane@example.com" }),
		);
		expect(await screen.findByText("User selected")).toBeTruthy();
		expect(
			screen.getByRole<HTMLButtonElement>("button", { name: "Add" }).disabled,
		).toBe(false);

		await settle();
		const settled = commits;
		await settle();
		expect(commits - settled).toBe(0);
	});
});
