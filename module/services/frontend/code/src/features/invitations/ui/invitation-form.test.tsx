import {
	cleanup,
	fireEvent,
	screen,
	within,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp } from "@/test/container";
import { InvitationForm } from "./invitation-form";

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
