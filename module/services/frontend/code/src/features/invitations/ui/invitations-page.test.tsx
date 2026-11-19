import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { InvitationsPage } from "./invitations-page";

const session = vi.hoisted(() => ({
	organizationId: "org-1",
	orgRole: "admin" as "admin" | "member",
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ ...session, switchOrganization: async () => {} }),
}));
afterEach(cleanup);
beforeEach(() => {
	session.orgRole = "admin";
	server.use(
		http.post(rpc("InvitationService", "ListInvitations"), () =>
			HttpResponse.json({
				invitations: [
					{
						id: "inv-1",
						orgId: "org-1",
						email: "member@example.com",
						status: 1,
						role: 1,
					},
				],
			}),
		),
	);
});

it("hides invitation mutations for a member", async () => {
	session.orgRole = "member";
	renderInApp(<InvitationsPage />);
	await screen.findByText("member@example.com");
	expect(screen.queryByRole("button", { name: "Invite" })).toBeNull();
	expect(
		screen.queryByRole("button", { name: /Revoke invitation/ }),
	).toBeNull();
	expect(
		screen.queryByRole("button", { name: /Resend invitation/ }),
	).toBeNull();
});

it("names the invitation, cancels without a request, and keeps a pending refusal in its dialog", async () => {
	let finish!: () => void;
	const pending = new Promise<void>((resolve) => {
		finish = resolve;
	});
	const revoke = vi.fn(async () => {
		await pending;
		return HttpResponse.json(
			{
				code: "permission_denied",
				message: "internal permission invitations:write",
			},
			{ status: 403 },
		);
	});
	server.use(http.post(rpc("InvitationService", "RevokeInvitation"), revoke));
	renderInApp(<InvitationsPage />);
	const trigger = await screen.findByRole("button", {
		name: "Revoke invitation to member@example.com",
	});
	fireEvent.click(trigger);
	let dialog = await screen.findByRole("alertdialog", {
		name: "Revoke invitation to member@example.com?",
	});
	fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
	await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	expect(revoke).not.toHaveBeenCalled();
	fireEvent.click(trigger);
	dialog = await screen.findByRole("alertdialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }));
	await waitFor(() => expect(revoke).toHaveBeenCalledOnce());
	expect(
		within(dialog)
			.getByRole("button", { name: "Cancel" })
			.hasAttribute("disabled"),
	).toBe(true);
	fireEvent.keyDown(dialog, { key: "Escape" });
	expect(screen.getByRole("alertdialog")).toBeTruthy();
	finish();
	expect((await within(dialog).findByRole("alert")).textContent).toContain(
		"Ask your organization administrator for access.",
	);
	expect(dialog.textContent).not.toContain("invitations:write");
});
