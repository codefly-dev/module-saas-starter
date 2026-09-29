import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "@/shared/ui";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { ManageMemberRolesDialog } from "./manage-member-roles-dialog";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-1", orgRole: "owner" }),
}));
afterEach(cleanup);

function open() {
	renderInApp(
		<ManageMemberRolesDialog
			orgId="org-1"
			userId="u-1"
			userLabel="jane@example.com"
			trigger={<Button>Manage roles</Button>}
		/>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Manage roles" }));
}

const denied = () =>
	HttpResponse.json(
		{ code: "permission_denied", message: "nope" },
		{ status: 403 },
	);

describe("the grants dialog says which of the two an empty list means", () => {
	// This is the dialog an administrator reaches from the roster's Shield, so
	// it is the primary place a grant is read. A refused read defaulting to []
	// reads as "this member holds nothing", which is the answer they act on.
	it("does not call a refused assignment read an unassigned member", async () => {
		server.use(
			http.post(rpc("PermissionService", "ListRoles"), () =>
				HttpResponse.json({ roles: [] }),
			),
			http.post(rpc("PermissionService", "ListRoleAssignments"), denied),
		);

		open();

		expect(
			await screen.findByText(
				/don't have permission to see this member's roles/,
			),
		).toBeTruthy();
		expect(screen.queryByText("No custom roles assigned.")).toBeNull();
	});

	// "No roles available" beside a working Create-a-role link would send the
	// reader off to duplicate a role they simply could not read.
	it("does not call a refused role list an organization with no roles", async () => {
		server.use(
			http.post(rpc("PermissionService", "ListRoles"), denied),
			http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
				HttpResponse.json({ assignments: [] }),
			),
		);

		open();

		expect(await screen.findByText("No custom roles assigned.")).toBeTruthy();
		fireEvent.click(screen.getByRole("combobox"));
		expect(
			await screen.findByText(
				/don't have permission to see this organization's roles/,
			),
		).toBeTruthy();
		expect(screen.queryByText("No roles available.")).toBeNull();
	});

	it("still says nothing is assigned when the read succeeded and nothing is", async () => {
		server.use(
			http.post(rpc("PermissionService", "ListRoles"), () =>
				HttpResponse.json({ roles: [] }),
			),
			http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
				HttpResponse.json({ assignments: [] }),
			),
		);

		open();

		expect(await screen.findByText("No custom roles assigned.")).toBeTruthy();
	});
});
