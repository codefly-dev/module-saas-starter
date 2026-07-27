"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	type SortingState,
	useReactTable,
} from "@tanstack/react-table";
import {
	MoreHorizontal,
	Pencil,
	ShieldAlert,
	ShieldOff,
	Trash2,
	UserCog,
} from "lucide-react";
import { useMemo, useState } from "react";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
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
import {
	getStatusBadgeVariant,
	statusLabel,
	toDisplayName,
} from "../model/transforms";
import type { User } from "../model/types";

const col = createColumnHelper<User>();

interface UsersTableProps {
	data: User[];
	isLoading: boolean;
	onEdit: (user: User) => void;
	onDelete: (user: User) => void;
	onSuspend: (user: User) => void;
	onUnsuspend: (user: User) => void;
	onImpersonate: (user: User) => void;
}

export function UsersTable({
	data,
	isLoading,
	onEdit,
	onDelete,
	onSuspend,
	onUnsuspend,
	onImpersonate,
}: UsersTableProps) {
	const [sorting, setSorting] = useState<SortingState>([]);

	const columns = useMemo(
		() => [
			col.accessor("primaryEmail", {
				header: "Email",
				cell: (info) => <span className="font-medium">{info.getValue()}</span>,
			}),
			col.display({
				id: "name",
				header: "Name",
				cell: ({ row }) =>
					toDisplayName(row.original.profile, row.original.primaryEmail),
			}),
			col.accessor("status", {
				header: "Status",
				cell: (info) => {
					const s = info.getValue();
					return (
						<Badge variant={getStatusBadgeVariant(s)}>{statusLabel(s)}</Badge>
					);
				},
			}),
			col.accessor("emailVerified", {
				header: "Verified",
				cell: (info) => (info.getValue() ? "Yes" : "No"),
			}),
			col.accessor("createdAt", {
				header: "Created",
				cell: (info) => (
					<span className="text-muted-foreground text-sm">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.display({
				id: "actions",
				cell: ({ row }) => {
					const user = row.original;
					return (
						<DropdownMenu>
							<DropdownMenuTrigger
								render={
									<Button variant="ghost" size="sm" className="h-8 w-8 p-0" />
								}
							>
								<MoreHorizontal className="h-4 w-4" />
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuGroup>
									<DropdownMenuLabel>Actions</DropdownMenuLabel>
									<DropdownMenuSeparator />
									<DropdownMenuItem onClick={() => onEdit(user)}>
										<Pencil className="mr-2 h-4 w-4" />
										Edit
									</DropdownMenuItem>
									{user.status === "active" && (
										<DropdownMenuItem onClick={() => onSuspend(user)}>
											<ShieldAlert className="mr-2 h-4 w-4" />
											Suspend
										</DropdownMenuItem>
									)}
									{user.status === "suspended" && (
										<DropdownMenuItem onClick={() => onUnsuspend(user)}>
											<ShieldOff className="mr-2 h-4 w-4" />
											Unsuspend
										</DropdownMenuItem>
									)}
									<DropdownMenuItem onClick={() => onImpersonate(user)}>
										<UserCog className="mr-2 h-4 w-4" />
										Impersonate
									</DropdownMenuItem>
									<DropdownMenuSeparator />
									<DropdownMenuItem
										onClick={() => onDelete(user)}
										className="text-destructive focus:text-destructive"
									>
										<Trash2 className="mr-2 h-4 w-4" />
										Delete
									</DropdownMenuItem>
								</DropdownMenuGroup>
							</DropdownMenuContent>
						</DropdownMenu>
					);
				},
			}),
		],
		[onEdit, onDelete, onSuspend, onUnsuspend, onImpersonate],
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
			emptyMessage="No users found."
		/>
	);
}
