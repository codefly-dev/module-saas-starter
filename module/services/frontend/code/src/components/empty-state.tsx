import { EmptyState as KitEmptyState } from "@codefly-dev/ui/layout";
import type { ComponentProps, ComponentType, ReactNode } from "react";

export function EmptyState({
	icon: Icon,
	title,
	headingAs,
	description,
	action,
	className,
}: {
	icon: ComponentType<{ className?: string }>;
	title: string;
	headingAs?: ComponentProps<typeof KitEmptyState>["headingAs"];
	description?: string;
	action?: ReactNode;
	className?: string;
}) {
	return (
		<KitEmptyState
			variant="illustrated"
			icon={<Icon />}
			heading={title}
			headingAs={headingAs}
			description={description}
			className={className}
		>
			{action}
		</KitEmptyState>
	);
}
