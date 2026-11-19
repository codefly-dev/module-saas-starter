import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { TeamForm } from "./team-form";

it("reports a whitespace name beside its field without submitting", async () => {
	const submit = vi.fn();
	render(
		<TeamForm open onSubmit={submit} onCancel={() => {}} isPending={false} />,
	);
	const name = screen.getByRole("textbox", { name: "Name" });
	fireEvent.change(name, { target: { value: "   " } });
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	expect(await screen.findByText("Name is required")).toBeTruthy();
	expect(name.getAttribute("aria-invalid")).toBe("true");
	expect(submit).not.toHaveBeenCalled();
	fireEvent.change(name, { target: { value: " Design " } });
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	await waitFor(() =>
		expect(submit).toHaveBeenCalledWith({ name: "Design", description: "" }),
	);
});
it("locks team fields and cancellation during submission", () => {
	const cancel = vi.fn();
	render(
		<TeamForm
			open
			initial={{ name: "Design" }}
			onSubmit={() => {}}
			onCancel={cancel}
			isPending
		/>,
	);
	expect(
		(screen.getByRole("textbox", { name: "Name" }) as HTMLInputElement)
			.disabled,
	).toBe(true);
	expect(
		(
			screen.getByRole("textbox", {
				name: "Description",
			}) as HTMLTextAreaElement
		).disabled,
	).toBe(true);
	fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
	expect(cancel).not.toHaveBeenCalled();
});
