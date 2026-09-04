import type * as React from "react";
import { Breadcrumbs, type Crumb } from "./breadcrumbs.js";
import { cn } from "./cn.js";

/** The top of a page: an optional breadcrumb trail, a title + description, and a
 * trailing action slot (buttons, a date range). Composes `Breadcrumbs`. */
function PageHeader({
	title,
	description,
	breadcrumbs,
	actions,
	className,
	...props
}: Omit<React.ComponentProps<"div">, "title"> & {
	title: React.ReactNode;
	description?: React.ReactNode;
	breadcrumbs?: Crumb[];
	actions?: React.ReactNode;
}) {
	return (
		<div
			data-slot="page-header"
			className={cn("space-y-3", className)}
			{...props}
		>
			{breadcrumbs && breadcrumbs.length > 0 && (
				<Breadcrumbs items={breadcrumbs} />
			)}
			<div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
				<div className="space-y-1">
					<h1 className="font-heading text-2xl font-semibold tracking-tight text-balance">
						{title}
					</h1>
					{description && (
						<p className="text-sm text-muted-foreground">{description}</p>
					)}
				</div>
				{actions && (
					<div className="flex shrink-0 items-center gap-2">{actions}</div>
				)}
			</div>
		</div>
	);
}

export { PageHeader };
