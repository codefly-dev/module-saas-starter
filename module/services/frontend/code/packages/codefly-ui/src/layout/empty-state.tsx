// A pure, tokens-driven empty-state block for solution pages and list surfaces.
// It replaces the heading + one-line-of-guidance that pages compose inline with a
// single consistent shape: a centered icon, heading, and description, with an
// optional trailing slot (children) for a call to action. No host context, no SDK,
// no data fetching — so the host app and a solution's Module-Federation remote
// render one shared instance.
//
// The icon defaults to a real glyph because a solution can't supply one itself:
// the kit owns `lucide-react`, so an empty state paints a proper icon out of the
// box and a caller overrides it with `icon` when a page-specific glyph fits better.

import { InboxIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "./cn.js";

export interface EmptyStateProps {
	/** Leading glyph; defaults to a generic inbox so the block always has one. */
	icon?: ReactNode;
	heading: ReactNode;
	description?: ReactNode;
	/** Optional trailing content, e.g. a call-to-action button. */
	children?: ReactNode;
	className?: string;
	variant?: "default" | "illustrated";
}

/** A centered placeholder for "nothing here yet" surfaces. */
export function EmptyState({
	icon = <InboxIcon />,
	heading,
	description,
	children,
	className,
	variant = "default",
}: EmptyStateProps) {
	return (
		<div
			data-slot="empty-state"
			className={cn(
				"flex flex-col items-center justify-center gap-2 px-6 py-12 text-center",
				variant === "illustrated" &&
					"relative overflow-hidden rounded-2xl border border-dashed bg-gradient-to-br from-muted/30 to-muted/0 py-16",
				className,
			)}
		>
			<div
				data-slot="empty-state-icon"
				className={cn(
					"text-muted-foreground [&_svg]:size-10 [&_svg]:[stroke-width:1.5]",
					variant === "illustrated" &&
						"relative mb-2 flex size-14 items-center justify-center rounded-2xl border bg-background shadow-sm [&_svg]:size-6",
				)}
			>
				{variant === "illustrated" && (
					<div
						aria-hidden
						className="absolute inset-0 -z-10 rounded-full bg-gradient-to-br from-primary/20 via-primary/10 to-accent blur-2xl"
					/>
				)}
				{icon}
			</div>
			<h3
				className={cn(
					"text-sm font-medium text-foreground",
					variant === "illustrated" && "text-lg font-semibold tracking-tight",
				)}
			>
				{heading}
			</h3>
			{description && (
				<p className="max-w-sm text-sm text-muted-foreground">{description}</p>
			)}
			{children}
		</div>
	);
}
