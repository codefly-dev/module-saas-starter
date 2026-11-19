import type { ReactNode } from "react";
import { cn } from "@/shared/lib/utils";
import { Panel } from "@/shared/ui";

/**
 * One part of a review or an access form: a named region under a heading in
 * the kit's type. A dialog's title is the level above, so these are its
 * third-level headings.
 */
export function ReviewSection({
	title,
	description,
	className,
	children,
}: {
	title: string;
	description?: ReactNode;
	className?: string;
	children: ReactNode;
}) {
	return (
		<section aria-label={title} className={cn("space-y-2", className)}>
			<div className="space-y-1">
				<h3 className="type-card-title-sm">{title}</h3>
				{description && <p className="text-muted-foreground">{description}</p>}
			</div>
			{children}
		</section>
	);
}

/** One role's card inside a section: what it is, then what is said or chosen about it. */
export function RolePanel({
	role,
	heading,
	children,
}: {
	/** The role's declared name, which names the region. */
	role: string;
	heading: ReactNode;
	children: ReactNode;
}) {
	return (
		<Panel
			role="region"
			aria-label={`Role ${role}`}
			className="space-y-2 bg-transparent p-3"
		>
			<h4 className="type-label">{heading}</h4>
			{children}
		</Panel>
	);
}
