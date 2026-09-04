"use client";

import { flexRender, type Table as TanStackTable } from "@tanstack/react-table";
import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";
import { Button } from "../layout/button.js";
import { Skeleton } from "../layout/skeleton.js";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "../layout/table.js";

export interface DataTableProps<T> {
	table: TanStackTable<T>;
	isLoading?: boolean;
	emptyMessage?: string;
	onRowClick?: (row: T) => void;
}

/**
 * Renders a resolved TanStack table: sortable headers, a loading skeleton, an
 * empty message, and prev/next pagination. The caller owns the table instance
 * (`useReactTable`) and its sorting/filtering/pagination state; this composes the
 * layout `Table`/`Button`/`Skeleton` to paint it. The single kit home for
 * `DataTable` (issue #451) — the host `@/shared/ui/data-table` re-exports it.
 */
export function DataTable<T>({
	table,
	isLoading,
	emptyMessage = "No results.",
	onRowClick,
}: DataTableProps<T>) {
	if (isLoading) {
		return (
			<div data-slot="data-table" className="rounded-md border">
				<Table>
					<TableHeader>
						<TableRow>
							{table.getAllColumns().map((col) => (
								<TableHead key={col.id}>
									<Skeleton className="h-4 w-24" />
								</TableHead>
							))}
						</TableRow>
					</TableHeader>
					<TableBody>
						{Array.from({ length: 5 }).map((_, i) => (
							<TableRow key={i}>
								{table.getAllColumns().map((col) => (
									<TableCell key={col.id}>
										<Skeleton className="h-4 w-full" />
									</TableCell>
								))}
							</TableRow>
						))}
					</TableBody>
				</Table>
			</div>
		);
	}

	return (
		<div data-slot="data-table">
			<div className="rounded-md border">
				<Table>
					<TableHeader>
						{table.getHeaderGroups().map((headerGroup) => (
							<TableRow key={headerGroup.id}>
								{headerGroup.headers.map((header) => (
									<TableHead
										key={header.id}
										className={
											header.column.getCanSort()
												? "cursor-pointer select-none"
												: ""
										}
										onClick={header.column.getToggleSortingHandler()}
									>
										{header.isPlaceholder ? null : (
											<div className="flex items-center gap-1">
												{flexRender(
													header.column.columnDef.header,
													header.getContext(),
												)}
												{{
													asc: " ↑",
													desc: " ↓",
												}[header.column.getIsSorted() as string] ?? null}
											</div>
										)}
									</TableHead>
								))}
							</TableRow>
						))}
					</TableHeader>
					<TableBody>
						{table.getRowModel().rows.length ? (
							table.getRowModel().rows.map((row) => (
								<TableRow
									key={row.id}
									data-state={row.getIsSelected() && "selected"}
									className={onRowClick ? "cursor-pointer" : ""}
									onClick={() => onRowClick?.(row.original)}
								>
									{row.getVisibleCells().map((cell) => (
										<TableCell key={cell.id}>
											{flexRender(
												cell.column.columnDef.cell,
												cell.getContext(),
											)}
										</TableCell>
									))}
								</TableRow>
							))
						) : (
							<TableRow>
								<TableCell
									colSpan={table.getAllColumns().length}
									className="h-24 text-center text-muted-foreground"
								>
									{emptyMessage}
								</TableCell>
							</TableRow>
						)}
					</TableBody>
				</Table>
			</div>

			{table.getPageCount() > 1 && (
				<div className="flex items-center justify-between px-2 py-4">
					<p className="text-sm text-muted-foreground">
						Page {table.getState().pagination.pageIndex + 1} of{" "}
						{table.getPageCount()}
					</p>
					<div className="flex items-center gap-2">
						<Button
							variant="outline"
							size="sm"
							onClick={() => table.previousPage()}
							disabled={!table.getCanPreviousPage()}
						>
							<ChevronLeftIcon className="h-4 w-4" />
							Previous
						</Button>
						<Button
							variant="outline"
							size="sm"
							onClick={() => table.nextPage()}
							disabled={!table.getCanNextPage()}
						>
							Next
							<ChevronRightIcon className="h-4 w-4" />
						</Button>
					</div>
				</div>
			)}
		</div>
	);
}
