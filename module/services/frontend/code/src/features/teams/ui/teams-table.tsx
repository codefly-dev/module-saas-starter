"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	type SortingState,
	useReactTable,
} from "@tanstack/react-table";
import { MoreHorizontal, Pencil, Trash2, Users } from "lucide-react";
import Link from "next/link";
import { useMemo, useState } from "react";
import { formatDate } from "@/shared/lib/utils";
import {
	Button,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { Team } from "../model/types";

const col = createColumnHelper<Team>();

interface TeamsTableProps {
	data: Team[];
	isLoading: boolean;
	canManage?: boolean;
	onViewMembers: (team: Team) => void;
	onRename: (team: Team) => void;
	onDelete: (team: Team) => void;
}

export function TeamsTable({
	data,
	isLoading,
	canManage = false,
	onViewMembers,
	onRename,
	onDelete,
}: TeamsTableProps) {
	const [sorting, setSorting] = useState<SortingState>([]);

	const columns = useMemo(
		() => [
			col.accessor("name", {
				header: "Name",
				cell: (info) => (
					<Link
						className="font-medium text-primary hover:underline"
						href={`/admin/teams/${encodeURIComponent(info.row.original.id)}`}
						onClick={(event) => event.stopPropagation()}
					>
						{info.getValue()}
					</Link>
				),
			}),
			col.accessor("description", {
				header: "Description",
				cell: (info) => (
					<span className="text-sm text-muted-foreground">
						{info.getValue() || "-"}
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
			col.display({
				id: "actions",
				cell: ({ row }) => {
					const team = row.original;
					return (
						<DropdownMenu>
							<DropdownMenuTrigger
								render={
									<Button
										variant="ghost"
										size="sm"
										className="h-8 w-8 p-0"
										aria-label={`Actions for ${team.name}`}
										onClick={(event) => event.stopPropagation()}
									/>
								}
							>
								<MoreHorizontal className="h-4 w-4" />
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuGroup>
									<DropdownMenuLabel>Actions</DropdownMenuLabel>
									<DropdownMenuSeparator />
									<DropdownMenuItem
										onClick={(event) => {
											event.stopPropagation();
											onViewMembers(team);
										}}
									>
										<Users className="mr-2 h-4 w-4" />
										View team
									</DropdownMenuItem>
									{canManage && (
										<DropdownMenuItem
											onClick={(event) => {
												event.stopPropagation();
												onRename(team);
											}}
										>
											<Pencil className="mr-2 h-4 w-4" />
											Rename
										</DropdownMenuItem>
									)}
									{canManage && <DropdownMenuSeparator />}
									{canManage && (
										<DropdownMenuItem
											onClick={(event) => {
												event.stopPropagation();
												onDelete(team);
											}}
											className="text-destructive focus:text-destructive"
										>
											<Trash2 className="mr-2 h-4 w-4" />
											Delete
										</DropdownMenuItem>
									)}
								</DropdownMenuGroup>
							</DropdownMenuContent>
						</DropdownMenu>
					);
				},
			}),
		],
		[onViewMembers, onRename, onDelete, canManage],
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
			table={table}
			isLoading={isLoading}
			onRowClick={onViewMembers}
			emptyMessage="No teams yet."
		/>
	);
}
