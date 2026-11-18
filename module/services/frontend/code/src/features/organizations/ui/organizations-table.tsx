"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	type SortingState,
	useReactTable,
} from "@tanstack/react-table";
import { useMemo, useState } from "react";
import { formatDate } from "@/shared/lib/utils";
import { Button } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { Organization } from "../model/types";

const col = createColumnHelper<Organization>();

interface OrganizationsTableProps {
	data: Organization[];
	isLoading: boolean;
	onViewMembers: (org: Organization) => void;
}

export function OrganizationsTable({
	data,
	isLoading,
	onViewMembers,
}: OrganizationsTableProps) {
	const [sorting, setSorting] = useState<SortingState>([]);

	const columns = useMemo(
		() => [
			col.accessor("name", {
				header: "Name",
				cell: ({ row }) => (
					<Button
						variant="link"
						className="h-auto px-0 text-left font-medium"
						aria-label={`Manage ${row.original.name}`}
						onClick={() => onViewMembers(row.original)}
					>
						{row.original.name}
					</Button>
				),
			}),
			col.accessor("slug", {
				header: "Address",
				cell: (info) => (
					<span className="font-mono text-sm text-muted-foreground">
						{info.getValue()}
					</span>
				),
			}),
			col.accessor("createdAt", {
				header: "Created",
				cell: (info) => (
					<span className="text-sm text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
		],
		[onViewMembers],
	);

	const table = useReactTable({
		data,
		columns,
		state: { sorting },
		onSortingChange: setSorting,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<DataTable
			ariaLabel="Organizations"
			table={table}
			isLoading={isLoading}
			emptyMessage="No organizations yet."
		/>
	);
}
