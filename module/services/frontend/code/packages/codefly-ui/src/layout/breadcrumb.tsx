"use client";

import { Fragment, type ReactNode } from "react";
import { Button } from "./button.js";
import { cn } from "./cn.js";

export interface BreadcrumbItem {
	id: string;
	label: ReactNode;
	href?: string;
	onNavigate?: () => void;
}
export interface BreadcrumbProps {
	items: readonly BreadcrumbItem[];
	label?: string;
	separator?: ReactNode;
	className?: string;
}
/** The final crumb names the current page and is never an action. Long trails wrap. */
export function Breadcrumb({
	items,
	label = "Breadcrumb",
	separator = "/",
	className,
}: BreadcrumbProps) {
	if (!items.length) return null;
	return (
		<nav aria-label={label} data-slot="breadcrumb" className={className}>
			<ol
				className="type-list-item"
				style={{
					display: "flex",
					alignItems: "center",
					flexWrap: "wrap",
					gap: "0.5rem",
					listStyle: "none",
					margin: 0,
					padding: 0,
				}}
			>
				{items.map((item, index) => (
					<Fragment key={item.id}>
						{index > 0 && (
							<li aria-hidden="true" className="text-muted-foreground">
								{separator}
							</li>
						)}
						<li
							className={cn(
								index < items.length - 1 && "text-muted-foreground",
							)}
						>
							{index === items.length - 1 ? (
								<span aria-current="page">{item.label}</span>
							) : item.href ? (
								<a
									href={item.href}
									onClick={
										item.onNavigate
											? (event) => {
													event.preventDefault();
													item.onNavigate?.();
												}
											: undefined
									}
									className="rounded-sm underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
								>
									{item.label}
								</a>
							) : item.onNavigate ? (
								<Button variant="link" onClick={item.onNavigate}>
									{item.label}
								</Button>
							) : (
								<span>{item.label}</span>
							)}
						</li>
					</Fragment>
				))}
			</ol>
		</nav>
	);
}
