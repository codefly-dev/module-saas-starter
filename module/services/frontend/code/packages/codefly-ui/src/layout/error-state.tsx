import { TriangleAlertIcon } from "lucide-react";
import type * as React from "react";
import { cn } from "./cn.js";

/** The failure counterpart to `EmptyState`: a destructive-toned panel for a
 * surface that could not load, with an optional retry action. */
function ErrorState({
	icon,
	title = "Something went wrong",
	description,
	actions,
	className,
	...props
}: React.ComponentProps<"div"> & {
	icon?: React.ReactNode;
	title?: React.ReactNode;
	description?: React.ReactNode;
	actions?: React.ReactNode;
}) {
	return (
		<div
			data-slot="error-state"
			role="alert"
			className={cn(
				"flex flex-col items-center justify-center gap-3 rounded-lg border border-destructive/30 bg-destructive/5 px-6 py-12 text-center",
				className,
			)}
			{...props}
		>
			<div className="flex size-10 items-center justify-center rounded-full bg-destructive/10 text-destructive [&_svg]:size-5">
				{icon ?? <TriangleAlertIcon aria-hidden />}
			</div>
			<div className="space-y-1">
				<h3 className="text-sm font-medium">{title}</h3>
				{description && (
					<p className="text-sm text-muted-foreground">{description}</p>
				)}
			</div>
			{actions && <div className="flex items-center gap-2">{actions}</div>}
		</div>
	);
}

export { ErrorState };
