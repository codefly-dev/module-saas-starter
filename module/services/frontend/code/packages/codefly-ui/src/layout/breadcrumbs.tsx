import { ChevronRightIcon } from "lucide-react";
import type * as React from "react";
import { cn } from "./cn.js";

export interface Crumb {
	label: React.ReactNode;
	/** Omit on the current (last) page; it renders as plain text. */
	href?: string;
}

/** A compact hierarchical trail. Data-in: pass `items`; the last is rendered as
 * the current page (`aria-current`), the rest as links. */
function Breadcrumbs({
	items,
	className,
	...props
}: Omit<React.ComponentProps<"nav">, "children"> & { items: Crumb[] }) {
	return (
		<nav
			data-slot="breadcrumbs"
			aria-label="Breadcrumb"
			className={cn(
				"flex items-center text-sm text-muted-foreground",
				className,
			)}
			{...props}
		>
			<ol className="flex flex-wrap items-center gap-1.5">
				{items.map((item, i) => {
					const last = i === items.length - 1;
					return (
						<li key={i} className="flex items-center gap-1.5">
							{item.href && !last ? (
								<a
									href={item.href}
									className="transition-colors hover:text-foreground"
								>
									{item.label}
								</a>
							) : (
								<span
									className={cn(last && "font-medium text-foreground")}
									aria-current={last ? "page" : undefined}
								>
									{item.label}
								</span>
							)}
							{!last && (
								<ChevronRightIcon className="size-3.5 shrink-0" aria-hidden />
							)}
						</li>
					);
				})}
			</ol>
		</nav>
	);
}

export { Breadcrumbs };
