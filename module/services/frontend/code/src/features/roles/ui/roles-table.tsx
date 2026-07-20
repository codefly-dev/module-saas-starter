"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Trash2 } from "lucide-react";
import { useMemo } from "react";
import { toast } from "sonner";
import { truncateUUID } from "@/shared/lib/utils";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
	Badge,
	Button,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { Role } from "../model/types";
import { useDeleteRole } from "../service/mutations";

const col = createColumnHelper<Role>();

export function RolesTable({
	roles,
	isLoading,
}: {
	roles: Role[];
	isLoading: boolean;
}) {
	const deleteRole = useDeleteRole();

	const columns = useMemo(
		() => [
			col.accessor("name", { header: "Name" }),
			col.accessor("description", {
				header: "Description",
				cell: (info) => (
					<span className="text-muted-foreground">
						{info.getValue() || "-"}
					</span>
				),
			}),
			col.accessor("permissions", {
				header: "Permissions",
				cell: (info) => {
					const perms = info.getValue();
					if (!perms || perms.length === 0) {
						return <span className="text-muted-foreground">None</span>;
					}
					return (
						<div className="flex flex-wrap gap-1">
							{perms.map((p, i) => (
								<Badge
									key={i}
									variant="secondary"
									className="font-mono text-xs"
								>
									{p.resource}:{p.action}
								</Badge>
							))}
						</div>
					);
				},
			}),
			col.accessor("builtIn", {
				header: "Type",
				cell: (info) =>
					info.getValue() ? (
						<Badge variant="outline">Built-in</Badge>
					) : (
						<Badge variant="secondary">Custom</Badge>
					),
			}),
			col.accessor("id", {
				header: "ID",
				cell: (info) => (
					<span className="font-mono text-xs text-muted-foreground">
						{truncateUUID(info.getValue())}
					</span>
				),
			}),
			col.display({
				id: "actions",
				header: "",
				cell: ({ row }) => {
					const role = row.original;
					if (role.builtIn) return null;
					return (
						<AlertDialog>
							<AlertDialogTrigger render={<Button variant="ghost" size="sm" />}>
								<Trash2 className="h-4 w-4 text-destructive" />
							</AlertDialogTrigger>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Delete role</AlertDialogTitle>
									<AlertDialogDescription>
										This will permanently delete the role &quot;{role.name}
										&quot; and remove all assignments. This action cannot be
										undone.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel>Cancel</AlertDialogCancel>
									<AlertDialogAction
										onClick={() =>
											deleteRole.mutate(role.id, {
												onSuccess: () =>
													toast.success(`Role "${role.name}" deleted`),
												onError: () => toast.error("Failed to delete role"),
											})
										}
									>
										Delete
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
					);
				},
			}),
		],
		[deleteRole],
	);

	const table = useReactTable({
		data: roles,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<DataTable
			table={table}
			isLoading={isLoading}
			emptyMessage="No roles defined"
		/>
	);
}
