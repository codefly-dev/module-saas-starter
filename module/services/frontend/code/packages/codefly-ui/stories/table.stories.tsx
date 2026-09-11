import { useState } from "react";
import {
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
	type SortingState,
} from "@tanstack/react-table";
import { DataTable } from "../src/table/index.js";

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
export const Paginated = { render: () => <ExampleTable /> };
export const Empty = { render: () => <ExampleTable empty /> };
export const Loading = { render: () => <ExampleTable loading /> };
