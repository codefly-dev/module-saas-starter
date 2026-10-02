import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

const authState = vi.hoisted(() => ({
	organizationId: "org-acme",
	user: { id: "user-1" },
	switchOrganization: vi.fn(async (_organizationId: string) => {}),
	logout: vi.fn(async () => {}),
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

import { DeleteOrganizationDialog } from "./delete-organization-dialog";
import { LeaveOrganizationDialog } from "./leave-organization-dialog";

const acme = { id: "org-acme", name: "Acme", slug: "acme" };

afterEach(() => {
	cleanup();
	authState.switchOrganization.mockClear();
	authState.logout.mockClear();
});

function organizations(list: { id: string; name: string; slug: string }[]) {
	server.use(
		http.post(rpc("OrganizationService", "ListOrganizations"), () =>
			HttpResponse.json({ organizations: list, canCreate: true }),
		),
	);
}

describe("LeaveOrganizationDialog", () => {
	it("moves the session to another organization before leaving the current one", async () => {
		const calls: string[] = [];
		authState.switchOrganization.mockImplementation(async (id: string) => {
			calls.push(`switch:${id}`);
		});
		organizations([acme, { id: "org-beta", name: "Beta", slug: "beta" }]);
		server.use(
			http.post(
				rpc("OrganizationService", "LeaveOrganization"),
				async ({ request }) => {
					calls.push(
						`leave:${((await request.json()) as { orgId: string }).orgId}`,
					);
					return HttpResponse.json({});
				},
			),
		);
		renderInApp(
			<LeaveOrganizationDialog
				organization={acme}
				open
				onOpenChange={() => {}}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Leave organization" }));
		await waitFor(() =>
			expect(calls).toEqual(["switch:org-beta", "leave:org-acme"]),
		);
		expect(authState.logout).not.toHaveBeenCalled();
	});

	it("shows the server's refusal for the sole member, with the way forward", async () => {
		organizations([acme, { id: "org-beta", name: "Beta", slug: "beta" }]);
		server.use(
			http.post(rpc("OrganizationService", "LeaveOrganization"), () =>
				HttpResponse.json(
					{
						code: "failed_precondition",
						message:
							"you are this organization's only member; delete it instead of leaving",
					},
					{ status: 400 },
				),
			),
		);
		renderInApp(
			<LeaveOrganizationDialog
				organization={acme}
				open
				onOpenChange={() => {}}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Leave organization" }));
		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain(
			"you are this organization's only member; delete it instead of leaving",
		);
		expect(screen.getByRole("link", { name: "Manage members" })).toBeTruthy();
	});
});

describe("DeleteOrganizationDialog", () => {
	it("deletes only once the slug is typed, and sends it as the confirmation", async () => {
		authState.organizationId = "org-other";
		let body: unknown;
		server.use(
			http.post(
				rpc("OrganizationService", "DeleteOrganization"),
				async ({ request }) => {
					body = await request.json();
					return HttpResponse.json({});
				},
			),
		);
		const onDeleted = vi.fn();
		renderInApp(
			<DeleteOrganizationDialog
				organization={acme}
				open
				onOpenChange={() => {}}
				onDeleted={onDeleted}
			/>,
		);
		const button = screen.getByRole("button", { name: "Delete organization" });
		expect((button as HTMLButtonElement).disabled).toBe(true);
		fireEvent.change(screen.getByLabelText(/to confirm/), {
			target: { value: "acme" },
		});
		expect((button as HTMLButtonElement).disabled).toBe(false);
		fireEvent.click(button);
		await waitFor(() => expect(onDeleted).toHaveBeenCalled());
		expect(body).toEqual({ orgId: "org-acme", confirmSlug: "acme" });
		// Not the session's organization: nothing to switch away from.
		expect(authState.switchOrganization).not.toHaveBeenCalled();
		authState.organizationId = "org-acme";
	});
});
