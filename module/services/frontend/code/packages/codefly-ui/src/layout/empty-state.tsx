import { InboxIcon } from "lucide-react";
import type * as React from "react";
import { cn } from "./cn.js";

/** A centered "nothing here yet" panel: an icon, a title, an optional
 * description, and optional actions (e.g. a primary `Button`). The first-class
 * version of the empties surfaces hand-roll inline. */
function EmptyState({
	icon,
	title,
	description,
	actions,
	className,
	...props
}: React.ComponentProps<"div"> & {
	icon?: React.ReactNode;
	title: React.ReactNode;
	description?: React.ReactNode;
	actions?: React.ReactNode;
}) {
	return (
		<div
			data-slot="empty-state"
			className={cn(
				"flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed px-6 py-12 text-center",
				className,
			)}
			{...props}
		>
			<div className="flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground [&_svg]:size-5">
				{icon ?? <InboxIcon aria-hidden />}
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

export { EmptyState };
