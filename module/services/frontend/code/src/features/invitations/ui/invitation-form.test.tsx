import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
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
	it("explains a usage limit beside the retained invitation", async () => {
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
		fireEvent.change(within(dialog).getByRole("textbox", { name: "Email" }), {
			target: { value: "sixth@example.com" },
		});
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Send invite" }),
		);
		expect((await within(dialog).findByRole("alert")).textContent).toContain(
			"A usage limit has been reached. Ask your organization administrator to review it.",
		);
		expect(
			(
				within(dialog).getByRole("textbox", {
					name: "Email",
				}) as HTMLInputElement
			).value,
		).toBe("sixth@example.com");
	});
});

describe("invitation submission", () => {
	it("validates the email beside its field before making a request", async () => {
		const create = vi.fn(() => HttpResponse.json({}));
		server.use(http.post(rpc("InvitationService", "CreateInvitation"), create));
		renderInApp(<InvitationForm orgId="org-example" />);
		fireEvent.click(screen.getByRole("button", { name: "Invite" }));
		const dialog = await screen.findByRole("dialog");
		const email = within(dialog).getByRole("textbox", { name: "Email" });
		fireEvent.change(email, { target: { value: "invalid" } });
		fireEvent.submit(
			within(dialog).getByRole("form", { name: "Invite member" }),
		);
		expect(email.getAttribute("aria-invalid")).toBe("true");
		expect(
			document.getElementById(email.getAttribute("aria-describedby")!)
				?.textContent,
		).toBe("Enter a valid email address.");
		expect(create).not.toHaveBeenCalled();
	});
	it("locks the draft and prevents dismissal or duplicate submission while sending", async () => {
		let finish!: () => void;
		const pending = new Promise<void>((resolve) => {
			finish = resolve;
		});
		const create = vi.fn(async () => {
			await pending;
			return HttpResponse.json({});
		});
		server.use(http.post(rpc("InvitationService", "CreateInvitation"), create));
		renderInApp(<InvitationForm orgId="org-example" />);
		fireEvent.click(screen.getByRole("button", { name: "Invite" }));
		const dialog = await screen.findByRole("dialog");
		const email = within(dialog).getByRole("textbox", { name: "Email" });
		fireEvent.change(email, { target: { value: "member@example.com" } });
		const form = within(dialog).getByRole("form", { name: "Invite member" });
		fireEvent.submit(form);
		fireEvent.submit(form);
		await waitFor(() => expect(create).toHaveBeenCalledOnce());
		expect(email.hasAttribute("disabled")).toBe(true);
		expect(
			within(dialog)
				.getByRole("button", { name: "Cancel" })
				.hasAttribute("disabled"),
		).toBe(true);
		fireEvent.keyDown(dialog, { key: "Escape" });
		expect(screen.getByRole("dialog")).toBeTruthy();
		finish();
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});
});
