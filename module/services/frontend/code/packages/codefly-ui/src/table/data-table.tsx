"use client";

import { flexRender, type Table as TanStackTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button } from "../layout/button.js";
import {
	type DelayedLoadingOptions,
	useLoadingPhase,
} from "../layout/delayed-loading.js";
import { Skeleton } from "../layout/skeleton.js";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "../layout/table.js";

interface DataTableProps<T> extends DelayedLoadingOptions {
	table: TanStackTable<T>;
	isLoading?: boolean;
	emptyMessage?: string;
	onRowClick?: (row: T) => void;
}

export function DataTable<T>({
	table,
	isLoading,
	emptyMessage = "No results.",
	onRowClick,
	// Inherited from `DelayedLoadingOptions`, and overridden for the same reason
	// the primitive allows it: an example or a test that exists to *show* the
	// skeleton needs it on screen at a known moment. The product never passes
	// these — the defaults are the rule.
	delayMs,
	minVisibleMs,
}: DataTableProps<T>) {
	"use no memo";
	// TanStack keeps the instance stable while its row model changes.
	//
	// Every table in the product renders through here, so this is where the
	// never-flash rule is kept rather than in each caller. `indicator` is the
	// skeleton's own two rules — nothing before 200ms, and once up it stays long
	// enough to read. `quiet` is the window before that, and it has to render
	// NOTHING: falling through it would reach the empty row and flash "No results."
	// over a table that is merely still loading, which is worse than the skeleton
	// the delay was there to spare the reader.
	const { indicator, quiet } = useLoadingPhase(!!isLoading, {
		...(delayMs === undefined ? {} : { delayMs }),
		...(minVisibleMs === undefined ? {} : { minVisibleMs }),
	});
	// The quiet window keeps the frame and its headers, and shows neither the
	// skeleton nor the empty row. Returning null here instead would satisfy both
	// rules and still be wrong: a query key that changes under a mounted table —
	// switching organization — puts `isLoading` back to true with the previous
	// rows on screen, so the table would collapse to nothing for 200ms and then
	// come back. The header is neither an indicator nor an answer about the data,
	// so holding it costs the reader nothing and keeps the layout still.
	if (quiet) return <DataTableSkeleton table={table} rows={0} />;
	if (indicator) return <DataTableSkeleton table={table} />;

	return (
		<div>
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
													asc: " \u2191",
													desc: " \u2193",
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

			{/* Pagination */}
			{table.getPageCount() > 1 && (
				<div className="flex items-center justify-between px-2 py-4">
					<p className="type-body text-muted-foreground">
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
							<ChevronLeft className="h-4 w-4" />
							Previous
						</Button>
						<Button
							variant="outline"
							size="sm"
							onClick={() => table.nextPage()}
							disabled={!table.getCanNextPage()}
						>
							Next
							<ChevronRight className="h-4 w-4" />
						</Button>
					</div>
				</div>
			)}
		</div>
	);
}

/**
 * The table's loading appearance, on its own.
 *
 * Separate from `DataTable` so the appearance and the *timing* of it are
 * independently visible: an example or a test that exists to show the skeleton
 * renders this, while `DataTable` owns when a wait has earned one. Rendering
 * this directly shows the skeleton with no delay, which is right for an example
 * and wrong for a product surface — those pass `isLoading` to `DataTable`.
 */
export function DataTableSkeleton<T>({
	table,
	rows = 5,
}: {
	table: TanStackTable<T>;
	/**
	 * Placeholder rows. `0` is the frame alone — the header and border with no
	 * body — which is what `DataTable` holds during the window before a wait has
	 * earned an indicator. It carries no `aria-busy` and no accessible name,
	 * because nothing is being announced yet.
	 */
	rows?: number;
}) {
	"use no memo";
	const announced = rows > 0;
	return (
		<div
			className="rounded-md border"
			{...(announced
				? { "aria-busy": true, "aria-label": "Loading table" }
				: {})}
		>
			<Table>
				<TableHeader>
					{table.getHeaderGroups().map((headerGroup) => (
						<TableRow key={headerGroup.id}>
							{headerGroup.headers.map((header) => (
								<TableHead key={header.id}>
									{/* Real column names in the frame-only form: a table's identity
									    is known before its rows are, and a header of blank boxes
									    holds the layout without saying what is coming. Bars only
									    once this is an announced indicator. */}
									{announced ? (
										<Skeleton className="h-4 w-24" />
									) : header.isPlaceholder ? null : (
										flexRender(
											header.column.columnDef.header,
											header.getContext(),
										)
									)}
								</TableHead>
							))}
						</TableRow>
					))}
				</TableHeader>
				<TableBody>
					{Array.from({ length: rows }).map((_, i) => (
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
