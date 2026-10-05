"use client";

import {
	CircleCheckIcon,
	InfoIcon,
	OctagonAlertIcon,
	TriangleAlertIcon,
	X,
} from "lucide-react";
import type { ReactNode } from "react";
import type { StatusTone } from "./badge.js";
import { Button } from "./button.js";
import { cn } from "./cn.js";

export interface BannerProps {
	title: ReactNode;
	children?: ReactNode;
	actions?: ReactNode;
	/**
	 * What kind of news this is. `neutral` is the product speaking and keeps
	 * the banner's original look; the others are statuses, each with its own
	 * glyph so the kind is told by shape and not by colour alone.
	 */
	tone?: StatusTone;
	/** Replaces the tone's glyph; `null` for none. */
	icon?: ReactNode;
	onDismiss?: () => void;
	dismissLabel?: string;
	dismissDisabled?: boolean;
	className?: string;
}

const toneClasses: Record<StatusTone, string> = {
	neutral: "border-primary/30 bg-primary/5",
	info: "border-info/30 bg-info/5 [&_[data-slot=banner-icon]]:text-info",
	success:
		"border-success/30 bg-success/5 [&_[data-slot=banner-icon]]:text-success",
	warning:
		"border-warning/40 bg-warning/10 [&_[data-slot=banner-icon]]:text-warning",
	danger:
		"border-destructive/40 bg-destructive/10 [&_[data-slot=banner-icon]]:text-destructive",
};

const toneIcons: Record<StatusTone, ReactNode> = {
	neutral: null,
	info: <InfoIcon />,
	success: <CircleCheckIcon />,
	warning: <TriangleAlertIcon />,
	danger: <OctagonAlertIcon />,
};

/** Persistent feedback. Callers own fetching, authorization and read state. */
export function Banner({
	title,
	children,
	actions,
	tone = "neutral",
	icon,
	onDismiss,
	dismissLabel = "Dismiss notification",
	dismissDisabled,
	className,
}: BannerProps) {
	const glyph = icon === undefined ? toneIcons[tone] : icon;
	// A fault interrupts; anything else waits for a pause in what is being read.
	const assertive = tone === "danger";
	return (
		<div
			data-slot="banner"
			data-tone={tone}
			role={assertive ? "alert" : "status"}
			aria-live={assertive ? "assertive" : "polite"}
			className={cn(
				"flex items-center justify-between gap-4 rounded-lg border px-4 py-3 type-banner",
				toneClasses[tone],
				className,
			)}
		>
			<div className="flex min-w-0 items-start gap-3">
				{glyph && (
					<span
						aria-hidden
						data-slot="banner-icon"
						className="mt-0.5 inline-flex shrink-0 [&_svg]:size-4"
					>
						{glyph}
					</span>
				)}
				<div className="min-w-0">
					<div
						data-slot="banner-title"
						className="type-banner-title text-foreground"
					>
						{title}
					</div>
					{children && <div className="text-muted-foreground">{children}</div>}
				</div>
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
