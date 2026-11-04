import { EmptyState as KitEmptyState } from "@codefly-dev/ui/layout";
import type { ComponentType, ReactNode } from "react";

export function EmptyState({
	icon: Icon,
	title,
	description,
	action,
	className,
	// The page's own emptiness is illustrated — a framed glyph in a dashed
	// panel. A section inside a page that is not otherwise empty takes
	// `"default"`, so a minor list's "nothing here" does not out-weigh the
	// content beside it.
	variant = "illustrated",
}: {
	icon: ComponentType<{ className?: string }>;
	title: string;
	description?: string;
	action?: ReactNode;
	className?: string;
	variant?: "default" | "illustrated";
}) {
	return (
		<KitEmptyState
			variant={variant}
			icon={<Icon />}
			heading={title}
			description={description}
			className={className}
		>
			{action}
		</KitEmptyState>
	);
}
