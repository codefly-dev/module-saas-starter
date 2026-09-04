// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DataTable, type DataTableColumn } from "../index.js";

afterEach(cleanup);

interface Row {
	name: string;
	city: string;
}
const columns: DataTableColumn<Row>[] = [
	{ key: "name", header: "Name", sortable: true },
	{ key: "city", header: "City" },
];
const data: Row[] = [
	{ name: "Jane", city: "NYC" },
	{ name: "Amir", city: "Berlin" },
];

describe("DataTable", () => {
	it("renders rows from data", () => {
		render(<DataTable columns={columns} data={data} />);
		expect(screen.getByText("Jane")).toBeTruthy();
		expect(screen.getByText("Berlin")).toBeTruthy();
	});

	it("shows skeleton rows while loading and no data", () => {
		const { container } = render(
			<DataTable columns={columns} data={[]} isLoading skeletonRows={3} />,
		);
		expect(container.querySelectorAll("[data-slot=skeleton]").length).toBe(6);
		expect(screen.queryByText("No results")).toBeNull();
	});

	it("shows an empty state when not loading and empty", () => {
		render(<DataTable columns={columns} data={[]} />);
		expect(screen.getByText("No results")).toBeTruthy();
	});

	it("toggles sort direction on a sortable header", () => {
		const onSortChange = vi.fn();
		render(
			<DataTable
				columns={columns}
				data={data}
				sort={{ key: "name", direction: "asc" }}
				onSortChange={onSortChange}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: /Name/ }));
		expect(onSortChange).toHaveBeenCalledWith({
			key: "name",
			direction: "desc",
		});
	});

	it("uses a custom cell renderer", () => {
		render(
			<DataTable
				columns={[{ key: "name", header: "Name", cell: (r) => `Hi ${r.name}` }]}
				data={data}
			/>,
		);
		expect(screen.getByText("Hi Jane")).toBeTruthy();
	});
});
