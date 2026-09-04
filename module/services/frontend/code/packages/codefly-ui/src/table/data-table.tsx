"use client";

import {
	ChevronDownIcon,
	ChevronUpIcon,
	ChevronsUpDownIcon,
} from "lucide-react";
import type * as React from "react";
import { cn } from "../layout/cn.js";
import { EmptyState } from "../layout/empty-state.js";
import { Pagination } from "../layout/pagination.js";
import { Skeleton } from "../layout/skeleton.js";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "../layout/table.js";

export type SortDirection = "asc" | "desc";
export interface SortState {
	key: string;
	direction: SortDirection;
}

export interface DataTableColumn<T> {
	key: string;
	header: React.ReactNode;
	/** Cell renderer; defaults to `String(row[key])`. */
	cell?: (row: T) => React.ReactNode;
	align?: "start" | "end";
	sortable?: boolean;
	/** Header/cell width utility, e.g. "w-40". */
	className?: string;
}

/**
 * A presentational table over rows: sortable headers, a loading skeleton, an
 * empty slot, and optional pagination — all **controlled** (it holds no state),
 * composing the layout `Table`. Data resolution, sorting, and paging live in the
 * caller; this renders the result.
 */
function DataTable<T>({
	columns,
	data,
	getRowId,
	sort,
	onSortChange,
	isLoading = false,
	skeletonRows = 5,
	empty,
	onRowClick,
	page,
	pageCount,
	onPageChange,
	className,
}: {
	columns: DataTableColumn<T>[];
	data: T[];
	getRowId?: (row: T, index: number) => string;
	sort?: SortState;
	onSortChange?: (sort: SortState) => void;
	isLoading?: boolean;
	skeletonRows?: number;
	empty?: React.ReactNode;
	onRowClick?: (row: T) => void;
	page?: number;
	pageCount?: number;
	onPageChange?: (page: number) => void;
	className?: string;
}) {
	const toggleSort = (key: string) => {
		if (!onSortChange) return;
		const direction: SortDirection =
			sort?.key === key && sort.direction === "asc" ? "desc" : "asc";
		onSortChange({ key, direction });
	};

	const showEmpty = !isLoading && data.length === 0;

	return (
		<div data-slot="data-table" className={cn("space-y-3", className)}>
			<div className="overflow-hidden rounded-lg border">
				<Table>
					<TableHeader>
						<TableRow>
							{columns.map((col) => {
								const active = sort?.key === col.key;
								return (
									<TableHead
										key={col.key}
										className={cn(
											col.align === "end" && "text-right",
											col.className,
										)}
									>
										{col.sortable ? (
											<button
												type="button"
												onClick={() => toggleSort(col.key)}
												className={cn(
													"inline-flex items-center gap-1 transition-colors hover:text-foreground [&_svg]:size-3.5 [&_svg]:text-muted-foreground",
													col.align === "end" && "flex-row-reverse",
													active && "text-foreground",
												)}
											>
												{col.header}
												{active ? (
													sort?.direction === "asc" ? (
														<ChevronUpIcon />
													) : (
														<ChevronDownIcon />
													)
												) : (
													<ChevronsUpDownIcon />
												)}
											</button>
										) : (
											col.header
										)}
									</TableHead>
								);
							})}
						</TableRow>
					</TableHeader>
					<TableBody>
						{isLoading &&
							Array.from({ length: skeletonRows }).map((_, r) => (
								<TableRow key={`sk-${r}`}>
									{columns.map((col) => (
										<TableCell key={col.key} className={col.className}>
											<Skeleton className="h-4 w-full max-w-32" />
										</TableCell>
									))}
								</TableRow>
							))}
						{!isLoading &&
							data.map((row, i) => (
								<TableRow
									key={getRowId?.(row, i) ?? i}
									onClick={onRowClick ? () => onRowClick(row) : undefined}
									className={cn(onRowClick && "cursor-pointer")}
								>
									{columns.map((col) => (
										<TableCell
											key={col.key}
											className={cn(
												col.align === "end" && "text-right tabular-nums",
												col.className,
											)}
										>
											{col.cell
												? col.cell(row)
												: String(
														(row as Record<string, unknown>)[col.key] ?? "",
													)}
										</TableCell>
									))}
								</TableRow>
							))}
					</TableBody>
				</Table>
				{showEmpty && (
					<div className="p-4">
						{empty ?? <EmptyState title="No results" />}
					</div>
				)}
			</div>
			{page !== undefined && pageCount !== undefined && pageCount > 1 && (
				<div className="flex justify-end">
					<Pagination
						page={page}
						pageCount={pageCount}
						onPageChange={onPageChange}
					/>
				</div>
			)}
		</div>
	);
}

export { DataTable };
