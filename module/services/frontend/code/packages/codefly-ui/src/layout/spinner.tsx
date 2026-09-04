import { cva, type VariantProps } from "class-variance-authority";
import { LoaderCircleIcon } from "lucide-react";
import type * as React from "react";
import { cn } from "./cn.js";

const spinnerVariants = cva("animate-spin text-muted-foreground", {
	variants: {
		size: {
			sm: "size-4",
			default: "size-5",
			lg: "size-8",
		},
	},
	defaultVariants: { size: "default" },
});

/** An indeterminate loading indicator. Pairs with `Skeleton` (which is for
 * content-shaped placeholders); use `Spinner` for a short, in-place wait. */
function Spinner({
	className,
	size,
	label = "Loading",
	...props
}: React.ComponentProps<"span"> &
	VariantProps<typeof spinnerVariants> & {
		label?: string;
	}) {
	return (
		<span
			data-slot="spinner"
			role="status"
			aria-live="polite"
			className={cn("inline-flex items-center gap-2", className)}
			{...props}
		>
			<LoaderCircleIcon className={cn(spinnerVariants({ size }))} aria-hidden />
			<span className="sr-only">{label}</span>
		</span>
	);
}

export { Spinner, spinnerVariants };
