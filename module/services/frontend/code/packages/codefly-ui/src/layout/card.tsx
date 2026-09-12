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
	/** Trailing controls rendered opposite the title (buttons, menus). */
	actions?: ReactNode;
	children?: ReactNode;
	className?: string;
}

/** A bordered surface with an optional title row and trailing actions. */
export function Card({ title, actions, children, className }: CardProps) {
	return (
		<CardRoot
			className={cn("gap-0 rounded-lg border p-4 shadow-sm ring-0", className)}
		>
			{(title || actions) && (
				<div className="mb-3 flex items-center justify-between gap-4">
					{title && <h3 className="text-base font-medium">{title}</h3>}
					{actions && <div className="flex items-center gap-2">{actions}</div>}
				</div>
			)}
			{children}
		</CardRoot>
	);
}
