// @vitest-environment happy-dom
import {
	getCoreRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { DataTable } from "./data-table";

afterEach(cleanup);
const data = [{ name: "Beta" }, { name: "Alpha" }];
function Example({
	loading = false,
	onOpen,
}: {
	loading?: boolean;
	onOpen?: (row: { name: string }) => void;
}) {
	const table = useReactTable({
		data,
		columns: [{ accessorKey: "name", header: "Name" }],
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
	});
	return (
		<DataTable
			table={table}
			ariaLabel="Organizations"
			isLoading={loading}
			onRowClick={onOpen}
		/>
	);
}
it("keeps its accessible name while waiting for rows", () => {
	render(<Example loading />);
	expect(screen.getByRole("table", { name: "Organizations" })).toBeTruthy();
	expect(screen.queryByText("No results.")).toBeNull();
});
it("sorts from the keyboard and announces direction", () => {
	render(<Example />);
	const header = screen.getByRole("columnheader", { name: "Name" });
	expect(header.tabIndex).toBe(0);
	fireEvent.keyDown(header, { key: "Enter" });
	expect(header.getAttribute("aria-sort")).toBe("ascending");
	expect(within(screen.getAllByRole("row")[1]).getByText("Alpha")).toBeTruthy();
	fireEvent.keyDown(header, { key: " " });
	expect(header.getAttribute("aria-sort")).toBe("descending");
});
it("opens a focusable row from the keyboard", () => {
	const onOpen = vi.fn();
	render(<Example onOpen={onOpen} />);
	const row = screen.getByText("Beta").closest("tr")!;
	expect(row.tabIndex).toBe(0);
	fireEvent.keyDown(row, { key: "Enter" });
	expect(onOpen).toHaveBeenCalledWith(data[0]);
});
