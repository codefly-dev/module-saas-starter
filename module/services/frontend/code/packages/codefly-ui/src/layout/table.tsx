"use client";

import type * as React from "react";

import { cn } from "./cn.js";

function Table({ className, ...props }: React.ComponentProps<"table">) {
	return (
		<div
			data-slot="table-container"
			className="relative w-full overflow-x-auto"
		>
			<table
				data-slot="table"
				className={cn("w-full caption-bottom type-table", className)}
				{...props}
			/>
		</div>
	);
}

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
	return (
		<thead
			data-slot="table-header"
			className={cn("[&_tr]:border-b", className)}
			{...props}
		/>
	);
}

function TableBody({ className, ...props }: React.ComponentProps<"tbody">) {
	return (
		<tbody
			data-slot="table-body"
			className={cn("[&_tr:last-child]:border-0", className)}
			{...props}
		/>
	);
}

function TableFooter({ className, ...props }: React.ComponentProps<"tfoot">) {
	return (
		<tfoot
			data-slot="table-footer"
			className={cn(
				"border-t bg-muted/50 type-table-footer [&>tr]:last:border-b-0",
				className,
			)}
			{...props}
		/>
	);
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
	return (
		<tr
			data-slot="table-row"
			className={cn(
				"border-b transition-colors hover:bg-muted/50 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-muted",
				className,
			)}
			{...props}
		/>
	);
}

function TableHead({ className, ...props }: React.ComponentProps<"th">) {
	return (
		<th
			data-slot="table-head"
			className={cn(
				"h-10 px-2 text-left align-middle type-table-head whitespace-nowrap text-foreground [&:has([role=checkbox])]:pr-0",
				className,
			)}
			{...props}
		/>
	);
}

function TableCell({ className, ...props }: React.ComponentProps<"td">) {
	return (
		<td
			data-slot="table-cell"
			className={cn(
				"p-2 align-middle whitespace-nowrap [&:has([role=checkbox])]:pr-0",
				className,
			)}
			{...props}
		/>
	);
}

function TableCaption({
	className,
	...props
}: React.ComponentProps<"caption">) {
	return (
		<caption
			data-slot="table-caption"
			className={cn("mt-4 type-table-caption text-muted-foreground", className)}
			{...props}
		/>
	);
}

/**
 * The row of filters and actions above a table. It is a sibling of the table
 * rather than part of it, because a `<table>` may not contain arbitrary markup
 * and a toolbar rendered inside one is invalid HTML the browser silently moves.
 */
function TableToolbar({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="table-toolbar"
			className={cn(
				"type-table-toolbar flex items-center justify-between gap-2 py-2",
				className,
			)}
			{...props}
		/>
	);
}

/**
 * The "nothing here" row. It spans every column, so it takes `colSpan` rather
 * than guessing: a wrong span leaves an empty cell beside the message and breaks
 * the row's border.
 */
function TableEmptyState({
	colSpan,
	className,
	children,
	...props
}: React.ComponentProps<"td"> & { colSpan: number }) {
	return (
		<tr data-slot="table-empty-state-row" className="hover:bg-transparent">
			<td
				colSpan={colSpan}
				data-slot="table-empty-state"
				className={cn(
					"type-table-empty-state p-6 text-center text-muted-foreground",
					className,
				)}
				{...props}
			>
				{children}
			</td>
		</tr>
	);
}

export {
	Table,
	TableBody,
	TableCaption,
	TableCell,
	TableEmptyState,
	TableFooter,
	TableHead,
	TableHeader,
	TableRow,
	TableToolbar,
};
