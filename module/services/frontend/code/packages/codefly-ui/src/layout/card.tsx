// Pure, tokens-driven page containers for solution pages. They take children and
// paint a bordered card or a titled section — no host context, no SDK, no data
// fetching — so the host app and a solution's Module-Federation remote compose
// pages from one shared package instance. No state or effects here, so these stay
// server-safe (no `"use client"`); only Tabs needs the client boundary.

import type { CSSProperties, ComponentProps, ReactNode } from "react";
import { cn } from "./cn.js";

// `title` is overridden to ReactNode (a card heading, not the HTML `title`
// tooltip string); everything else on a <div> — onClick, id, style, data-*,
// aria-* — passes straight through so a composer like StatTile can wire them.
export interface CardProps extends Omit<ComponentProps<"div">, "title"> {
	title?: ReactNode;
	/** Trailing controls rendered opposite the title (buttons, menus). */
	actions?: ReactNode;
}

/** A bordered surface with an optional title row and trailing actions. */
export function Card({
	title,
	actions,
	children,
	className,
	...props
}: CardProps) {
	return (
		<div
			className={cn(
				"rounded-lg border bg-card p-4 text-card-foreground shadow-sm",
				className,
			)}
			{...props}
		>
			{(title || actions) && (
				<div className="mb-3 flex items-center justify-between gap-4">
					{title && <h3 className="text-base font-medium">{title}</h3>}
					{actions && <div className="flex items-center gap-2">{actions}</div>}
				</div>
			)}
			{children}
		</div>
	);
}

export interface SectionProps {
	title?: ReactNode;
	description?: ReactNode;
	children?: ReactNode;
	className?: string;
	/** Inline style on the section root, e.g. to scope a CSS-variable override. */
	style?: CSSProperties;
}

/** A titled block of page content with an optional description. */
export function Section({
	title,
	description,
	children,
	className,
	style,
}: SectionProps) {
	return (
		<section className={cn("space-y-4", className)} style={style}>
			{(title || description) && (
				<div className="space-y-1">
					{title && (
						<h2 className="text-lg font-semibold tracking-tight">{title}</h2>
					)}
					{description && (
						<p className="text-sm text-muted-foreground">{description}</p>
					)}
				</div>
			)}
			{children}
		</section>
	);
}
