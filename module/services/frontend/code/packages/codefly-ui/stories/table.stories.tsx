import {
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	type SortingState,
	useReactTable,
} from "@tanstack/react-table";
import { useState } from "react";
import { DataTable, DataTableSkeleton } from "../src/table/index.js";

export default { title: "Shared UI/Data table" };
const rows = [
	{ name: "Acme", members: 3 },
	{ name: "ExampleCorp", members: 7 },
	{ name: "Example workspace", members: 1 },
];
function ExampleTable({
	empty = false,
	loading = false,
}: {
	empty?: boolean;
	loading?: boolean;
}) {
	const [sorting, setSorting] = useState<SortingState>([]);
	const table = useReactTable({
		data: empty ? [] : rows,
		columns: [
			{ accessorKey: "name", header: "Workspace" },
			{ accessorKey: "members", header: "Members" },
		],
		state: { sorting },
		onSortingChange: setSorting,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
		initialState: { pagination: { pageSize: 2 } },
	});
	return (
		<DataTable
			table={table}
			isLoading={loading}
			emptyMessage="No workspaces found."
		/>
	);
}
function LoadingTable() {
	const table = useReactTable({
		data: [] as typeof rows,
		columns: [
			{ accessorKey: "name", header: "Workspace" },
			{ accessorKey: "members", header: "Members" },
		],
		getCoreRowModel: getCoreRowModel(),
	});
	return <DataTableSkeleton table={table} />;
}
export const Paginated = { render: () => <ExampleTable /> };
export const Empty = { render: () => <ExampleTable empty /> };
// The appearance, shown directly. `DataTable` owns *when* it appears (nothing
// for the first 200ms of a wait), which is a timing rule a story cannot show
// and `data-table.test.tsx` asserts instead.
export const Loading = { render: () => <LoadingTable /> };
