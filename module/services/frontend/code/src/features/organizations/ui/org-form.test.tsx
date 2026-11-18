import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { OrgForm } from "./org-form";

afterEach(cleanup);

test("connects validation to fields and preserves an edited organization address", async () => {
	const submit = vi.fn();
	render(
		<OrgForm open onSubmit={submit} onCancel={vi.fn()} isPending={false} />,
	);
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	await waitFor(() =>
		expect(
			screen
				.getByRole("textbox", { name: /Name/ })
				.getAttribute("aria-invalid"),
		).toBe("true"),
	);
	expect(submit).not.toHaveBeenCalled();
	const name = screen.getByRole("textbox", { name: /Name/ });
	const address = screen.getByRole("textbox", { name: /Organization address/ });
	fireEvent.change(name, { target: { value: "Acme Company" } });
	expect((address as HTMLInputElement).value).toBe("acme-company");
	fireEvent.change(name, { target: { value: "Acme" } });
	expect((address as HTMLInputElement).value).toBe("acme");
	fireEvent.change(address, { target: { value: "custom-address" } });
	fireEvent.change(name, { target: { value: "Acme Organization" } });
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	await waitFor(() =>
		expect(submit).toHaveBeenCalledWith({
			name: "Acme Organization",
			slug: "custom-address",
		}),
	);
});

test("blocks editing and dismissal while creation is pending", () => {
	const cancel = vi.fn();
	const submit = vi.fn();
	render(<OrgForm open onSubmit={submit} onCancel={cancel} isPending />);
	for (const field of screen.getAllByRole("textbox"))
		expect((field as HTMLInputElement).disabled).toBe(true);
	expect(
		(screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement)
			.disabled,
	).toBe(true);
	fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
	expect(cancel).not.toHaveBeenCalled();
	expect(submit).not.toHaveBeenCalled();
});

test("keeps a failed creation visible beside the editable draft", () => {
	render(
		<OrgForm
			open
			onSubmit={vi.fn()}
			onCancel={vi.fn()}
			isPending={false}
			error="Ask your organization administrator for access."
		/>,
	);
	expect(screen.getByText("Couldn't create the organization")).toBeTruthy();
	expect(
		screen.getByText("Ask your organization administrator for access."),
	).toBeTruthy();
	expect(
		(screen.getByRole("textbox", { name: /Name/ }) as HTMLInputElement)
			.disabled,
	).toBe(false);
});
