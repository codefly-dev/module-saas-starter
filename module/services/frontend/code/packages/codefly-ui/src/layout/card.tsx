// Pure, tokens-driven page containers for solution pages. They take children and
// paint a bordered card or a titled section — no host context, no SDK, no data
// fetching — so the host app and a solution's Module-Federation remote compose
// pages from one shared package instance. No state or effects here, so these stay
// server-safe (no `"use client"`); only Tabs needs the client boundary.

import type { ReactNode } from "react";
import { cn } from "./cn.js";
import { CardRoot } from "./card-root.js";

export interface CardProps {
	title?: ReactNode;
	/**
	 * A line under the title saying what this card is, matching `Section`'s
	 * prop of the same name.
	 *
	 * Without it a caller needing one either reaches for `CardRoot` +
	 * `CardDescription` — mixing two card shapes into one page, which is how a
	 * page stops looking like one page — or drops the sentence into the body as
	 * a muted paragraph, where it reads as content rather than as the card's
	 * own description and no longer carries the `card-description` slot.
	 */
	description?: ReactNode;
	/** Trailing controls rendered opposite the title (buttons, menus). */
	actions?: ReactNode;
	children?: ReactNode;
	className?: string;
}

/** A bordered surface with an optional title row and trailing actions. */
export function Card({
	title,
	description,
	actions,
	children,
	className,
}: CardProps) {
	const hasHeader = title != null || description != null || actions != null;
	return (
		<CardRoot
			className={cn("gap-0 rounded-lg border p-4 shadow-sm ring-0", className)}
		>
			{hasHeader && (
				// `items-start`, not `items-center`: with a description the heading
				// block is two lines tall and centring would float the actions
				// against the middle of it. Matches `Section`'s header.
				<div className="mb-3 flex items-start justify-between gap-4">
					<div className="space-y-1">
						{title != null && (
							<h3 data-slot="card-heading" className="type-card-heading">
								{title}
							</h3>
						)}
						{description != null && (
							<p
								data-slot="card-description"
								className="type-card-description text-muted-foreground"
							>
								{description}
							</p>
						)}
					</div>
					{actions != null && (
						<div className="flex shrink-0 items-center gap-2">{actions}</div>
					)}
				</div>
			)}
			{children}
		</CardRoot>
	);
}
