import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { CreateOrganizationDialog } from "./create-organization-dialog";

afterEach(cleanup);

it("validates the organization address beside its kit field before sending", async () => {
	const create = vi.fn(() => HttpResponse.json({}));
	server.use(
		http.post(rpc("OrganizationService", "CreateOrganization"), create),
	);
	renderInApp(<CreateOrganizationDialog open onOpenChange={vi.fn()} />);
	fireEvent.change(screen.getByLabelText("Name"), {
		target: { value: "Acme" },
	});
	fireEvent.change(screen.getByLabelText("Organization address"), {
		target: { value: "Invalid address!" },
	});
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	expect(
		await screen.findByText(
			"Use lowercase letters, numbers and hyphens for the organization address",
		),
	).toBeTruthy();
	expect(
		screen.getByLabelText("Organization address").getAttribute("aria-invalid"),
	).toBe("true");
	expect(create).not.toHaveBeenCalled();
});

it("locks the draft while creating, then retains it on a safe inline failure", async () => {
	let finish!: () => void;
	const pending = new Promise<void>((resolve) => {
		finish = resolve;
	});
	const create = vi.fn(async () => {
		await pending;
		return HttpResponse.json(
			{ code: "unavailable", message: "private-server-detail" },
			{ status: 503 },
		);
	});
	server.use(
		http.post(rpc("OrganizationService", "CreateOrganization"), create),
	);
	const close = vi.fn();
	renderInApp(<CreateOrganizationDialog open onOpenChange={close} />);
	fireEvent.change(screen.getByLabelText("Name"), {
		target: { value: "Acme" },
	});
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
	expect((screen.getByLabelText("Name") as HTMLInputElement).disabled).toBe(
		true,
	);
	expect((screen.getByLabelText("User") as HTMLInputElement).disabled).toBe(
		true,
	);
	expect(
		(screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement)
			.disabled,
	).toBe(true);
	fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
	expect(close).not.toHaveBeenCalled();
	finish();
	expect(await screen.findByText("Organization wasn’t created")).toBeTruthy();
	expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe(
		"Acme",
	);
	expect(screen.queryByText("private-server-detail")).toBeNull();
});
