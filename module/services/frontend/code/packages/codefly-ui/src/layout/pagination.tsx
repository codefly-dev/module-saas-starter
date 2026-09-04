import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";
import type * as React from "react";
import { Button } from "./button.js";
import { cn } from "./cn.js";

// A page list with at most one gap on each side of the current page, e.g.
// 1 … 4 [5] 6 … 20. Always shows the first and last page.
function pageItems(page: number, pageCount: number): (number | "gap")[] {
	if (pageCount <= 7) return Array.from({ length: pageCount }, (_, i) => i + 1);
	const items = new Set([1, pageCount, page, page - 1, page + 1]);
	const sorted = [...items]
		.filter((p) => p >= 1 && p <= pageCount)
		.sort((a, b) => a - b);
	const out: (number | "gap")[] = [];
	let prev = 0;
	for (const p of sorted) {
		if (p - prev > 1) out.push("gap");
		out.push(p);
		prev = p;
	}
	return out;
}

/** Controlled pagination: give it the current `page` (1-based) and `pageCount`;
 * it calls `onPageChange`. Pure — it holds no state. */
function Pagination({
	page,
	pageCount,
	onPageChange,
	className,
	...props
}: Omit<React.ComponentProps<"nav">, "onChange"> & {
	page: number;
	pageCount: number;
	onPageChange?: (page: number) => void;
}) {
	if (pageCount <= 1) return null;
	return (
		<nav
			data-slot="pagination"
			aria-label="Pagination"
			className={cn("flex items-center gap-1", className)}
			{...props}
		>
			<Button
				variant="ghost"
				size="icon-sm"
				aria-label="Previous page"
				disabled={page <= 1}
				onClick={() => onPageChange?.(page - 1)}
			>
				<ChevronLeftIcon />
			</Button>
			{pageItems(page, pageCount).map((item, i) =>
				item === "gap" ? (
					<span
						key={`gap-${i}`}
						className="px-1.5 text-sm text-muted-foreground"
						aria-hidden
					>
						…
					</span>
				) : (
					<Button
						key={item}
						variant={item === page ? "default" : "ghost"}
						size="icon-sm"
						aria-label={`Page ${item}`}
						aria-current={item === page ? "page" : undefined}
						onClick={() => onPageChange?.(item)}
					>
						{item}
					</Button>
				),
			)}
			<Button
				variant="ghost"
				size="icon-sm"
				aria-label="Next page"
				disabled={page >= pageCount}
				onClick={() => onPageChange?.(page + 1)}
			>
				<ChevronRightIcon />
			</Button>
		</nav>
	);
}

export { Pagination };
