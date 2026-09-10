import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { OrgMembersPanel } from "./org-members-panel";

const { toast } = vi.hoisted(() => ({
	toast: { success: vi.fn(), error: vi.fn() },
}));
vi.mock("sonner", () => ({ toast }));

afterEach(() => {
	cleanup();
	toast.error.mockClear();
	toast.success.mockClear();
});

function withMembers() {
	server.use(
		http.post(rpc("OrganizationService", "ListMembers"), () =>
			HttpResponse.json({
				members: [{ orgId: "org-1", userId: "user-1", role: 3 }],
			}),
		),
	);
}

// The server refuses the change on a stated rule. Showing "Failed to remove
// member" would send the admin looking for an outage rather than at the rule,
// so the reason has to survive the whole round trip.
it("shows the server's reason when a removal is refused on principle", async () => {
	withMembers();
	server.use(
		http.post(rpc("OrganizationService", "RemoveMember"), () =>
			HttpResponse.json(
				{
					code: "failed_precondition",
					message: "organization must keep at least one owner or admin",
				},
				{ status: 412 },
			),
		),
	);

	renderInApp(
		<OrgMembersPanel orgId="org-1" orgName="Acme" onClose={() => {}} />,
	);
	await userEvent.click(await screen.findByLabelText("Remove member"));

	await waitFor(() =>
		expect(toast.error).toHaveBeenCalledWith(
			"organization must keep at least one owner or admin",
		),
	);
});

// A fault with no message meant for a user stays generic rather than leaking
// transport text into the UI.
it("falls back to a generic message for an unexplained failure", async () => {
	withMembers();
	server.use(
		http.post(rpc("OrganizationService", "RemoveMember"), () =>
			HttpResponse.json(
				{ code: "internal", message: "pq: connection reset by peer" },
				{ status: 500 },
			),
		),
	);

	renderInApp(
		<OrgMembersPanel orgId="org-1" orgName="Acme" onClose={() => {}} />,
	);
	await userEvent.click(await screen.findByLabelText("Remove member"));

	await waitFor(() =>
		expect(toast.error).toHaveBeenCalledWith("Failed to remove member"),
	);
});

describe("add member", () => {
	it("shows the server's reason when a role update is refused", async () => {
		withMembers();
		server.use(
			http.post(rpc("OrganizationService", "AddMember"), () =>
				HttpResponse.json(
					{
						code: "failed_precondition",
						message: "organization must keep at least one owner or admin",
					},
					{ status: 412 },
				),
			),
		);

		renderInApp(
			<OrgMembersPanel orgId="org-1" orgName="Acme" onClose={() => {}} />,
		);
		await userEvent.type(
			await screen.findByPlaceholderText("User ID to add..."),
			"user-1",
		);
		await userEvent.click(screen.getByRole("button", { name: /Add/ }));

		await waitFor(() =>
			expect(toast.error).toHaveBeenCalledWith(
				"organization must keep at least one owner or admin",
			),
		);
	});
});
