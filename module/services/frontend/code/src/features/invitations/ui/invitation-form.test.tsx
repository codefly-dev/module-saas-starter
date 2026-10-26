import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { InvitationForm } from "./invitation-form";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(cleanup);

describe("invitation role labels", () => {
	it("shows Member before opening the picker and Admin after selecting it", async () => {
		renderInApp(<InvitationForm orgId="org-example" />);
		fireEvent.click(screen.getByRole("button", { name: "Invite" }));
		const dialog = await screen.findByRole("dialog");
		const role = within(dialog).getByRole("combobox", { name: "Role" });
		expect(role.textContent).toContain("Member");
		fireEvent.click(role);
		const admin = await screen.findByRole("option", { name: "Admin" });
		fireEvent.pointerDown(admin, { pointerType: "mouse" });
		fireEvent.click(admin);
		await waitFor(() => expect(role.textContent).toContain("Admin"));
	});
});

describe("a refused invitation", () => {
	// "Failed to create invitation" sent an administrator looking for an
	// outage when the organization had simply used every seat it has.
	it("shows the quota the host refused it with", async () => {
		server.use(
			http.post(rpc("InvitationService", "CreateInvitation"), () =>
				HttpResponse.json(
					{
						code: "resource_exhausted",
						message: "seats quota exhausted: used 5 of 5",
					},
					{ status: 429 },
				),
			),
		);
		renderInApp(<InvitationForm orgId="org-example" />);
		fireEvent.click(screen.getByRole("button", { name: "Invite" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByLabelText("Email"), {
			target: { value: "sixth@example.com" },
		});
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Send Invite" }),
		);
		await waitFor(() =>
			expect(toast.error).toHaveBeenCalledWith(
				"seats quota exhausted: used 5 of 5",
			),
		);
	});
});
