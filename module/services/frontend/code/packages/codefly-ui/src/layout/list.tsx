// Lists for the places a `Table` is too heavy: a handful of rows with one or
// two things to say each (diagnostics, a relation list), and term/value pairs
// (a front-matter card). Pure and server-safe, like Card and Section.

import type { ReactNode } from "react";
import { cn } from "./cn.js";

export interface ListProps {
	children: ReactNode;
	/** `divided` rules a line between rows, for rows that each carry a description. */
	variant?: "plain" | "divided";
	/** Names the list for a screen reader when no heading above it does. */
	label?: string;
	className?: string;
}

export function List({
	children,
	variant = "plain",
	label,
	className,
}: ListProps) {
	return (
		// `role="list"`: Safari drops list semantics from an unstyled `ul`.
		<ul
			role="list"
			data-slot="list"
			data-variant={variant}
			aria-label={label}
			className={cn(
				"flex flex-col",
				variant === "plain"
					? "gap-2"
					: "divide-y [&>li]:py-2 [&>li:first-child]:pt-0 [&>li:last-child]:pb-0",
				className,
			)}
		>
			{children}
		</ul>
	);
}

export interface ListItemProps {
	/** The row's primary text. */
	children: ReactNode;
	icon?: ReactNode;
	description?: ReactNode;
	/** Trailing, quieter text: a time, a count, a badge. */
	meta?: ReactNode;
	/** Trailing controls, after `meta`. */
	actions?: ReactNode;
	className?: string;
}

export function ListItem({
	children,
	icon,
	description,
	meta,
	actions,
	className,
}: ListItemProps) {
	return (
		<li
			data-slot="list-item"
			className={cn("flex items-start gap-3 type-list-item", className)}
		>
			{icon && (
				<span
					aria-hidden
					data-slot="list-item-icon"
					className="mt-0.5 inline-flex shrink-0 text-muted-foreground [&_svg]:size-4"
				>
					{icon}
				</span>
			)}
			<div className="min-w-0 flex-1">
				<div data-slot="list-item-title" className="break-words">
					{children}
				</div>
				{description != null && (
					<div
						data-slot="list-item-description"
						className="type-list-item-description break-words text-muted-foreground"
					>
						{description}
					</div>
				)}
			</div>
			{meta != null && (
				<div
					data-slot="list-item-meta"
					className="shrink-0 type-caption-plain text-muted-foreground"
				>
					{meta}
				</div>
			)}
			{actions != null && (
				<div
					data-slot="list-item-actions"
					className="flex shrink-0 items-center gap-2"
				>
					{actions}
				</div>
			)}
		</li>
	);
}

export interface DescriptionListItem {
	term: ReactNode;
	value: ReactNode;
	/** Needed only when the list reorders; two rows may share a term. */
	key?: string;
}

export interface DescriptionListProps {
	items: readonly DescriptionListItem[];
	/**
	 * `inline` puts each term beside its value in two aligned columns, for a
	 * card of short facts; `stacked` puts the term above, for values that wrap.
	 */
	layout?: "inline" | "stacked";
	className?: string;
}

export function DescriptionList({
	items,
	layout = "inline",
	className,
}: DescriptionListProps) {
	return (
		<dl
			data-slot="description-list"
			data-layout={layout}
			className={cn(
				"grid",
				layout === "inline"
					? "grid-cols-[minmax(0,max-content)_minmax(0,1fr)] items-baseline gap-x-4 gap-y-2"
					: "gap-3",
				className,
			)}
		>
			{items.map((item, index) => (
				// A `div` around each pair is valid inside `dl`; `contents` lets the
				// inline layout's two columns align across every pair.
				<div
					key={item.key ?? index}
					data-slot="description-item"
					className={layout === "inline" ? "contents" : "space-y-0.5"}
				>
					<dt
						data-slot="description-term"
						className="type-description-term text-muted-foreground"
					>
						{item.term}
					</dt>
					<dd
						data-slot="description-details"
						className="min-w-0 type-description-details break-words"
					>
						{item.value}
					</dd>
				</div>
			))}
		</dl>
	);
}
