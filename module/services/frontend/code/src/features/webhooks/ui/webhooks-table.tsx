"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	type SortingState,
	useReactTable,
} from "@tanstack/react-table";
import { Key, MoreHorizontal, Send, Trash2 } from "lucide-react";
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
import { formatEventType } from "../model/transforms";
import type { WebhookSubscription } from "../model/types";

const col = createColumnHelper<WebhookSubscription>();

interface WebhooksTableProps {
	data: WebhookSubscription[];
	isLoading: boolean;
	onTest: (webhook: WebhookSubscription) => void;
	onDelete: (webhook: WebhookSubscription) => void;
	onSelect: (webhook: WebhookSubscription) => void;
	onRotateSecret: (webhook: WebhookSubscription) => void;
}

export function WebhooksTable({
	data,
	isLoading,
	onTest,
	onDelete,
	onSelect,
	onRotateSecret,
}: WebhooksTableProps) {
	const [sorting, setSorting] = useState<SortingState>([]);

	const columns = useMemo(
		() => [
			col.accessor("url", {
				header: "URL",
				cell: (info) => (
					<span className="font-mono text-sm">{info.getValue()}</span>
				),
			}),
			col.accessor("events", {
				header: "Events",
				cell: (info) => (
					<div className="flex flex-wrap gap-1">
						{info
							.getValue()
							.slice(0, 3)
							.map((event) => (
								<Badge key={event} variant="secondary" className="text-xs">
									{formatEventType(event)}
								</Badge>
							))}
						{info.getValue().length > 3 && (
							<Badge variant="outline" className="text-xs">
								+{info.getValue().length - 3}
							</Badge>
						)}
					</div>
				),
				enableSorting: false,
			}),
			col.accessor("active", {
				header: "Status",
				cell: (info) => (
					<Badge variant={info.getValue() ? "default" : "secondary"}>
						{info.getValue() ? "Active" : "Inactive"}
					</Badge>
				),
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
					const webhook = row.original;
					return (
						<div onClick={(event) => event.stopPropagation()}>
							<DropdownMenu>
								<DropdownMenuTrigger
									render={
										<Button
											variant="ghost"
											size="sm"
											className="h-8 w-8 p-0"
											aria-label={`Actions for ${webhook.url}`}
										/>
									}
								>
									<MoreHorizontal className="h-4 w-4" />
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end">
									<DropdownMenuGroup>
										<DropdownMenuLabel>Actions</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<DropdownMenuItem onClick={() => onTest(webhook)}>
											<Send className="mr-2 h-4 w-4" />
											Test
										</DropdownMenuItem>
										<DropdownMenuItem onClick={() => onRotateSecret(webhook)}>
											<Key className="mr-2 h-4 w-4" />
											Rotate secret
										</DropdownMenuItem>
										<DropdownMenuSeparator />
										<DropdownMenuItem
											onClick={() => onDelete(webhook)}
											className="text-destructive"
										>
											<Trash2 className="mr-2 h-4 w-4" />
											Delete
										</DropdownMenuItem>
									</DropdownMenuGroup>
								</DropdownMenuContent>
							</DropdownMenu>
						</div>
					);
				},
			}),
		],
		[onTest, onDelete, onRotateSecret],
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
			emptyMessage="No webhooks configured."
			onRowClick={onSelect}
		/>
	);
}
