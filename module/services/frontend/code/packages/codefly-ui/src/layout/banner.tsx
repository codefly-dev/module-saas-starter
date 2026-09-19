"use client";

import { X } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "./button.js";
import { cn } from "./cn.js";

export interface BannerProps {
	title: ReactNode;
	children?: ReactNode;
	actions?: ReactNode;
	onDismiss?: () => void;
	dismissLabel?: string;
	dismissDisabled?: boolean;
	className?: string;
}

/** Persistent, polite feedback. Callers own fetching, authorization and read state. */
export function Banner({
	title,
	children,
	actions,
	onDismiss,
	dismissLabel = "Dismiss notification",
	dismissDisabled,
	className,
}: BannerProps) {
	return (
		<div
			data-slot="banner"
			role="status"
			aria-live="polite"
			className={cn(
				"flex items-center justify-between gap-4 rounded-lg border border-primary/30 bg-primary/5 px-4 py-3 type-banner",
				className,
			)}
		>
			<div className="min-w-0">
				<div
					data-slot="banner-title"
					className="type-banner-title text-foreground"
				>
					{title}
				</div>
				{children && <div className="text-muted-foreground">{children}</div>}
			</div>
			{(actions || onDismiss) && (
				<div className="flex shrink-0 items-center gap-2">
					{actions}
					{onDismiss && (
						<Button
							variant="ghost"
							size="sm"
							className="h-8 w-8 p-0"
							disabled={dismissDisabled}
							onClick={onDismiss}
							aria-label={dismissLabel}
						>
							<X className="h-4 w-4" />
						</Button>
					)}
				</div>
			)}
		</div>
	);
}
