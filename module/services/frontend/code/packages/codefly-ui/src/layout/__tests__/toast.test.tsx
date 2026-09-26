// @vitest-environment happy-dom
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { Toaster, toast } from "../index.js";

afterEach(cleanup);

it("raises a notification in the kit's Toaster through the kit's own toast", async () => {
	render(<Toaster />);
	await act(async () => {
		toast.error("Couldn't delete this chat: HTTP 503. Try again.");
	});
	expect(
		await screen.findByText("Couldn't delete this chat: HTTP 503. Try again."),
	).toBeTruthy();
});
