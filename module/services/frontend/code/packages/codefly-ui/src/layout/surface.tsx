import type { ComponentProps } from "react";
import { cn } from "./cn.js";

/** A painted surface with no imposed padding, direction, gap or shadow. */
export function Surface({ className, ...props }: ComponentProps<"div">) {
	return (
		<div
			data-slot="surface"
			className={cn(
				"rounded-lg border border-border bg-card text-card-foreground",
				className,
			)}
			{...props}
		/>
	);
}
