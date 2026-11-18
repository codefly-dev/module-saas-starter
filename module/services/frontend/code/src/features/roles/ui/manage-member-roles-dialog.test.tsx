import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "@/shared/ui";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { ManageMemberRolesDialog } from "./manage-member-roles-dialog";
import { RevokeRoleButton } from "./revoke-role-button";

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
		expect(
			(
				screen.getByRole("combobox", {
					name: "Grant a role",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
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

it("locks the role picker while assigning and keeps a refusal beside it", async () => {
	let release = () => {};
	server.use(
		http.post(rpc("PermissionService", "ListRoles"), () =>
			HttpResponse.json({ roles: [{ id: "role-1", name: "Support" }] }),
		),
		http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
			HttpResponse.json({ assignments: [] }),
		),
		http.post(rpc("PermissionService", "AssignRole"), async () => {
			await new Promise<void>((resolve) => {
				release = resolve;
			});
			return denied();
		}),
	);
	open();
	await screen.findByText("No custom roles assigned.");
	const picker = screen.getByRole("combobox", { name: "Grant a role" });
	await waitFor(() =>
		expect((picker as HTMLButtonElement).disabled).toBe(false),
	);
	fireEvent.click(picker);
	fireEvent.click(await screen.findByRole("option", { name: /Support/ }));
	fireEvent.click(screen.getByRole("button", { name: "Assign" }));
	try {
		await screen.findByRole("button", { name: "Assigning…" });
		expect((picker as HTMLButtonElement).disabled).toBe(true);
		expect(
			(screen.getByRole("button", { name: "Done" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);
		fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
		expect(screen.getByRole("dialog")).toBeTruthy();
	} finally {
		release();
	}
	await waitFor(() => expect(picker.getAttribute("aria-invalid")).toBe("true"));
	expect(screen.queryByText("nope")).toBeNull();
});

it("confirms the named role removal and retains a failed outcome in its dialog", async () => {
	let release = () => {};
	let calls = 0;
	server.use(
		http.post(rpc("PermissionService", "RevokeRole"), async () => {
			calls += 1;
			await new Promise<void>((resolve) => {
				release = resolve;
			});
			return denied();
		}),
	);
	renderInApp(
		<RevokeRoleButton
			subjectId="u-1"
			subjectLabel="Jane Doe"
			roleId="role-1"
			roleName="Support"
			orgId="org-1"
		/>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Remove Support" }));
	expect(screen.getByRole("alertdialog").textContent).toContain("Jane Doe");
	fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
	expect(calls).toBe(0);
	fireEvent.click(screen.getByRole("button", { name: "Remove Support" }));
	fireEvent.click(screen.getByRole("button", { name: "Remove role" }));
	try {
		await waitFor(() => expect(calls).toBe(1));
		expect(
			(screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);
		fireEvent.keyDown(screen.getByRole("alertdialog"), { key: "Escape" });
		expect(screen.getByRole("alertdialog")).toBeTruthy();
	} finally {
		release();
	}
	await screen.findByText("Couldn’t remove this role");
	expect(screen.getByRole("alertdialog")).toBeTruthy();
	expect(screen.queryByText("nope")).toBeNull();
});
