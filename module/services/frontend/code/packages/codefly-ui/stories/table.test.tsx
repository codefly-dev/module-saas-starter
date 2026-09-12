// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { Paginated, Empty, Loading } from "./table.stories";
afterEach(cleanup);
it("preserves sorting and pagination on the injected table instance", () => {
	const Story = Paginated.render;
	render(<Story />);
	expect(screen.getByText("Acme")).toBeTruthy();
	fireEvent.click(screen.getByRole("button", { name: "Next" }));
	expect(screen.getByText("Page 2 of 2")).toBeTruthy();
	expect(screen.getByText("Example workspace")).toBeTruthy();
	expect(
		screen.getByRole("button", { name: "Next" }).hasAttribute("disabled"),
	).toBe(true);
	fireEvent.click(screen.getByRole("button", { name: "Previous" }));
	fireEvent.click(screen.getByRole("columnheader", { name: "Members" }));
	expect(screen.getAllByRole("row")[1].textContent).toContain("ExampleCorp");
});
it("distinguishes loading from a resolved empty result", () => {
	const { rerender } = render(<Loading.render />);
	expect(screen.getByLabelText("Loading table").getAttribute("aria-busy")).toBe(
		"true",
	);
	expect(screen.queryByText("No workspaces found.")).toBeNull();
	rerender(<Empty.render />);
	expect(screen.getByText("No workspaces found.")).toBeTruthy();
	expect(screen.queryByLabelText("Loading table")).toBeNull();
});
