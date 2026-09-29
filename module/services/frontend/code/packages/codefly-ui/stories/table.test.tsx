// @vitest-environment happy-dom

import { getCoreRowModel, useReactTable } from "@tanstack/react-table";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
	LOADING_DELAY_MS,
	LOADING_MIN_VISIBLE_MS,
} from "../src/layout/delayed-loading.js";
import { DataTable } from "../src/table/index.js";
import { Empty, Loading, Paginated } from "./table.stories";

afterEach(cleanup);

// The clock is driven rather than polled: every boundary below is exact, and
// `shouldAdvanceTime` would add the real milliseconds between render and the
// first advance to the wait, failing only under load. Installed per-test before
// the render that schedules the delay, since a timer scheduled on the real clock
// is not one a later fake clock can advance.
const advance = async (ms: number) => {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
	});
};
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
// A table driven the way a product surface drives one: the wait is a prop, and
// the component stays mounted across it. That is what makes the minimum-visible
// floor observable — gating the *mount* on the wait instead would unmount the
// indicator the moment the answer landed, taking the floor's state with it.
function ToggleTable({ isLoading }: { isLoading: boolean }) {
	const table = useReactTable({
		data: [] as { name: string; members: number }[],
		columns: [{ accessorKey: "name", header: "Workspace" }],
		getCoreRowModel: getCoreRowModel(),
	});
	return (
		<DataTable
			table={table}
			isLoading={isLoading}
			emptyMessage="No workspaces found."
		/>
	);
}

const skeleton = () => screen.queryByLabelText("Loading table");
const emptyRow = () => screen.queryByText("No workspaces found.");

it("shows nothing at all — not even the empty message — before the delay", async () => {
	vi.useFakeTimers();
	try {
		const { rerender } = render(<ToggleTable isLoading />);
		expect(skeleton()).toBeNull();
		// The one that matters. Falling through the pre-delay window would reach
		// the empty row and tell the reader "No workspaces found." about a table
		// that is still loading — not noise, a wrong answer.
		expect(emptyRow()).toBeNull();

		await advance(LOADING_DELAY_MS - 1);
		expect(skeleton()).toBeNull();
		expect(emptyRow()).toBeNull();

		// A wait that ends inside the window shows nothing, ever.
		rerender(<ToggleTable isLoading={false} />);
		await advance(LOADING_MIN_VISIBLE_MS);
		expect(skeleton()).toBeNull();
		expect(emptyRow()).toBeTruthy();
	} finally {
		vi.useRealTimers();
	}
});

it("keeps a skeleton it has shown up long enough to read", async () => {
	vi.useFakeTimers();
	try {
		const { rerender } = render(<ToggleTable isLoading />);
		await advance(LOADING_DELAY_MS);
		expect(skeleton()).toBeTruthy();
		expect(emptyRow()).toBeNull();

		// The answer lands 1ms after the skeleton appeared. Without the floor this
		// is the blink the delay was supposed to prevent, moved later.
		await advance(1);
		rerender(<ToggleTable isLoading={false} />);
		expect(skeleton()).toBeTruthy();

		// The floor is measured from when the skeleton appeared, and 1ms of it is
		// already spent above, so 299 remain.
		await advance(LOADING_MIN_VISIBLE_MS - 2);
		expect(skeleton()).toBeTruthy();
		await advance(1);
		expect(skeleton()).toBeNull();
		expect(emptyRow()).toBeTruthy();
	} finally {
		vi.useRealTimers();
	}
});

it("renders the skeleton's appearance on its own, with no timing", () => {
	render(<Loading.render />);
	expect(skeleton()).toBeTruthy();
});

it("shows a resolved empty result as such", () => {
	render(<Empty.render />);
	expect(emptyRow()).toBeTruthy();
	expect(skeleton()).toBeNull();
});
