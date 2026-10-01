import { cleanup, fireEvent, screen, within } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { InvitationDeliveryStatus } from "@/gen/saas/accounts/v1/invitations_pb";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { formatDeliveryStatus } from "../model/transforms";
import type { Invitation } from "../model/types";
import { InvitationsTable } from "./invitations-table";

afterEach(cleanup);

const pending: Invitation = {
	id: "inv-1",
	orgId: "org-1",
	inviterId: "user-1",
	inviterDisplayName: "Jane Doe",
	email: "user@example.com",
	role: 1,
	status: 1,
	deliveryStatus: InvitationDeliveryStatus.DISABLED,
	sendCount: 0,
};

describe("invitation links", () => {
	it("does not describe an unsent invitation as waiting on a provider", () => {
		expect(formatDeliveryStatus(InvitationDeliveryStatus.DISABLED)).toBe(
			"Email not sent — share the link",
		);
	});

	it("hands the administrator a fresh accept link to share", async () => {
		server.use(
			http.post(rpc("InvitationService", "IssueInvitationLink"), () =>
				HttpResponse.json({
					invitation: { id: "inv-1", email: "user@example.com" },
					acceptUrl: "https://app.example.com/invitations/accept?token=abc",
				}),
			),
		);
		renderInApp(<InvitationsTable invitations={[pending]} isLoading={false} />);
		fireEvent.click(
			screen.getByRole("button", {
				name: "Copy invitation link for user@example.com",
			}),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			(within(dialog).getByLabelText("Accept link") as HTMLInputElement).value,
		).toBe("https://app.example.com/invitations/accept?token=abc");
	});
});
