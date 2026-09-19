"use client";

import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";
import type * as React from "react";

import { Button } from "./button.js";
import { cn } from "./cn.js";
import {
	PAGE_GAP,
	type PaginationEntry,
	paginationRange,
} from "./pagination-model.js";

// A pager over the shared control rungs, so it lines up with the toolbar it sits
// in. Which numbers appear is `paginationRange`, tested on its own; this file is
// only the rendering of that list.

export interface PaginationProps
	extends Omit<React.ComponentProps<"nav">, "onChange"> {
	page: number;
	pageCount: number;
	onPageChange: (page: number) => void;
	siblings?: number;
	boundaries?: number;
	size?: "xs" | "sm" | "default";
	/** Announced to assistive technology; the visible control has no heading. */
	label?: string;
}

function Pagination({
	page,
	pageCount,
	onPageChange,
	siblings,
	boundaries,
	size = "sm",
	label = "Pagination",
	className,
	...props
}: PaginationProps) {
	const entries = paginationRange({ page, pageCount, siblings, boundaries });
	if (entries.length === 0) return null;
	const current = Math.min(Math.max(page, 1), pageCount);

	return (
		<nav
			aria-label={label}
			data-slot="pagination"
			className={cn("flex items-center gap-1", className)}
			{...props}
		>
			<Button
				variant="ghost"
				size={size === "default" ? "icon" : `icon-${size}`}
				aria-label="Previous page"
				disabled={current <= 1}
				onClick={() => onPageChange(current - 1)}
			>
				<ChevronLeftIcon />
			</Button>
			{entries.map((entry: PaginationEntry, index) =>
				entry === PAGE_GAP ? (
					<span
						// Gaps carry no page number, so their position is the only stable
						// key; the list is regenerated wholesale on every page change.
						key={`gap-${index}`}
						aria-hidden="true"
						data-slot="pagination-ellipsis"
						className="type-pagination-ellipsis flex items-center justify-center px-1 text-muted-foreground"
					>
						…
					</span>
				) : (
					<Button
						key={entry}
						variant={entry === current ? "secondary" : "ghost"}
						size={size}
						// A page number is a Button, so its type comes from the rung the
						// button is sized at; it needs no slot of its own.
						data-slot="pagination-item"
						aria-label={`Page ${entry}`}
						aria-current={entry === current ? "page" : undefined}
						onClick={() => onPageChange(entry)}
					>
						{entry}
					</Button>
				),
			)}
			<Button
				variant="ghost"
				size={size === "default" ? "icon" : `icon-${size}`}
				aria-label="Next page"
				disabled={current >= pageCount}
				onClick={() => onPageChange(current + 1)}
			>
				<ChevronRightIcon />
			</Button>
		</nav>
	);
}

export { Pagination };
