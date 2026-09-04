// A pure, tokens-driven inline error block for solution pages. It unifies the
// bare `text-destructive` one-liners pages hand-roll into one shape: a leading
// alert glyph and a title in `--destructive`, with the failing detail secondary
// in `--muted-foreground`. No host context, no SDK, no data fetching — so the
// host app and a solution's Module-Federation remote render one shared instance.

import { CircleAlertIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "./cn.js";

export interface ErrorStateProps {
	/** The failure, in `--destructive`. */
	title: ReactNode;
	/** Secondary explanation of what failed, in `--muted-foreground`. */
	detail?: ReactNode;
	/** Leading glyph; defaults to an alert circle. */
	icon?: ReactNode;
	className?: string;
}

/** An inline error message: destructive title over a muted detail. */
export function ErrorState({
	title,
	detail,
	icon = <CircleAlertIcon />,
	className,
}: ErrorStateProps) {
	return (
		<div
			data-slot="error-state"
			role="alert"
			className={cn("flex items-start gap-2 text-sm", className)}
		>
			<div
				data-slot="error-state-icon"
				className="text-destructive [&_svg]:size-4 [&_svg]:translate-y-0.5"
			>
				{icon}
			</div>
			<div className="space-y-0.5">
				<p className="font-medium text-destructive">{title}</p>
				{detail && <p className="text-muted-foreground">{detail}</p>}
			</div>
		</div>
	);
}
