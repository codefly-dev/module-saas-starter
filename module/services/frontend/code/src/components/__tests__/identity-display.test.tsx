import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { OrgSelector } from "../org-selector";
import { UserLabel } from "../user-label";
import { UserPicker } from "../user-picker";

const auth = vi.hoisted(() => ({
	platformRole: "super_admin" as string | undefined,
	organizationId: "11111111-1111-4111-8111-111111111111",
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => auth }));
const userId = "22222222-2222-4222-8222-222222222222";
afterEach(cleanup);
beforeEach(() => {
	auth.platformRole = "super_admin";
});

describe("human-readable identity", () => {
	it("shows the selected organization name before opening the dropdown", async () => {
		server.use(
			http.post(rpc("OrganizationService", "ListOrganizations"), () =>
				HttpResponse.json({
					organizations: [
						{ id: auth.organizationId, name: "Acme", slug: "acme" },
					],
				}),
			),
		);
		renderInApp(<OrgSelector />);
		expect(screen.queryByText(auth.organizationId)).toBeNull();
		expect(await screen.findByText("Acme")).toBeTruthy();
		expect(screen.queryByText(auth.organizationId)).toBeNull();
	});
	it("resolves an admin-visible user to their name and email", async () => {
		server.use(
			http.post(rpc("UserService", "GetUser"), () =>
				HttpResponse.json({
					uuid: userId,
					primaryEmail: "jane@example.com",
					profile: { name: "Jane Doe" },
				}),
			),
		);
		renderInApp(<UserLabel userId={userId} />);
		expect(await screen.findByText("Jane Doe (jane@example.com)")).toBeTruthy();
		expect(screen.queryByText(userId)).toBeNull();
	});
	it("uses the tenant directory instead of attempting a privileged user lookup", async () => {
		auth.platformRole = undefined;
		const lookup = vi.fn(() => HttpResponse.json({}, { status: 403 }));
		server.use(
			http.post(rpc("UserService", "GetUser"), lookup),
			http.post(rpc("OrganizationService", "ListMembers"), () =>
				HttpResponse.json({
					members: [
						{
							orgId: auth.organizationId,
							userId,
							userEmail: "jane@example.com",
						},
					],
				}),
			),
		);
		renderInApp(<UserLabel userId={userId} />);
		expect(await screen.findByText("jane@example.com")).toBeTruthy();
		expect(lookup).not.toHaveBeenCalled();
	});
	it("never falls back to displaying an inaccessible account's UUID", async () => {
		server.use(
			http.post(rpc("UserService", "GetUser"), () =>
				HttpResponse.json(
					{ code: "not_found", message: "User not found" },
					{ status: 404 },
				),
			),
		);
		renderInApp(<UserLabel userId={userId} />);
		expect(await screen.findByText("User unavailable")).toBeTruthy();
		expect(screen.queryByText(userId)).toBeNull();
	});
	it("selects a person by email while passing the internal ID to the action", async () => {
		const selected = vi.fn();
		server.use(
			http.post(rpc("PlatformAdminService", "SearchUsers"), () =>
				HttpResponse.json({
					users: [
						{
							uuid: userId,
							primaryEmail: "jane@example.com",
							profile: { name: "Jane Doe" },
						},
					],
				}),
			),
		);
		function Picker() {
			const [value, setValue] = useState("");
			return (
				<UserPicker
					value={value}
					onChange={(id) => {
						setValue(id);
						selected(id);
					}}
				/>
			);
		}
		renderInApp(<Picker />);
		fireEvent.change(screen.getByLabelText("User"), {
			target: { value: "jane" },
		});
		fireEvent.click(
			await screen.findByRole("button", {
				name: "Jane Doe (jane@example.com)",
			}),
		);
		expect(selected).toHaveBeenLastCalledWith(userId);
		expect(screen.queryByText(userId)).toBeNull();
	});
});
