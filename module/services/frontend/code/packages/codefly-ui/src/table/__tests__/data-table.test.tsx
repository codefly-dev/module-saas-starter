// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { afterEach, describe, expect, it } from "vitest";
import { DataTable } from "../index.js";

afterEach(cleanup);

interface Row {
	name: string;
	city: string;
}
const col = createColumnHelper<Row>();
const columns = [
	col.accessor("name", { header: "Name" }),
	col.accessor("city", { header: "City" }),
];

function Harness({
	data,
	isLoading,
	emptyMessage,
	pageSize,
}: {
	data: Row[];
	isLoading?: boolean;
	emptyMessage?: string;
	pageSize?: number;
}) {
	const table = useReactTable({
		data,
		columns,
		getCoreRowModel: getCoreRowModel(),
		...(pageSize
			? {
					getPaginationRowModel: getPaginationRowModel(),
					initialState: { pagination: { pageIndex: 0, pageSize } },
				}
			: {}),
	});
	return (
		<DataTable
			table={table}
			isLoading={isLoading}
			emptyMessage={emptyMessage}
		/>
	);
}

const data: Row[] = [
	{ name: "Jane", city: "NYC" },
	{ name: "Amir", city: "Berlin" },
];

describe("DataTable (kit, TanStack)", () => {
	it("renders rows from the table instance", () => {
		render(<Harness data={data} />);
		expect(screen.getByText("Jane")).toBeTruthy();
		expect(screen.getByText("Berlin")).toBeTruthy();
	});

	it("shows skeleton placeholders while loading", () => {
		const { container } = render(<Harness data={[]} isLoading />);
		// 2 header-cell skeletons + 5 body rows × 2 columns.
		expect(container.querySelectorAll("[data-slot=skeleton]").length).toBe(12);
	});

	it("shows the empty message when there are no rows", () => {
		render(<Harness data={[]} emptyMessage="No people" />);
		expect(screen.getByText("No people")).toBeTruthy();
	});

	it("shows pagination controls only when there is more than one page", () => {
		const many = Array.from({ length: 15 }, (_, i) => ({
			name: `n${i}`,
			city: `c${i}`,
		}));
		render(<Harness data={many} pageSize={10} />);
		expect(screen.getByText("Page 1 of 2")).toBeTruthy();
		expect(screen.getByRole("button", { name: /Next/ })).toBeTruthy();
	});

	it("hides pagination for a single page", () => {
		render(<Harness data={data} />);
		expect(screen.queryByRole("button", { name: /Next/ })).toBeNull();
	});
});
