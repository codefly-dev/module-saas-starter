import { EmptyState as KitEmptyState } from "@codefly-dev/ui/layout";
import type { ComponentType, ReactNode } from "react";

export function EmptyState({
	icon: Icon,
	title,
	description,
	action,
	className,
}: {
	icon: ComponentType<{ className?: string }>;
	title: string;
	description?: string;
	action?: ReactNode;
	className?: string;
}) {
	return (
		<KitEmptyState
			variant="illustrated"
			icon={<Icon />}
			heading={title}
			description={description}
			className={className}
		>
			{action}
		</KitEmptyState>
	);
}
