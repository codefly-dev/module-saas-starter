import { cva, type VariantProps } from "class-variance-authority";
import {
	CircleAlertIcon,
	CircleCheckIcon,
	InfoIcon,
	TriangleAlertIcon,
} from "lucide-react";
import type * as React from "react";
import { cn } from "./cn.js";

const bannerVariants = cva(
	"flex items-start gap-3 rounded-lg border px-4 py-3 text-sm [&_svg]:mt-0.5 [&_svg]:size-4 [&_svg]:shrink-0",
	{
		variants: {
			tone: {
				info: "border-border bg-muted/50 text-foreground [&_svg]:text-muted-foreground",
				success:
					"border-[color-mix(in_oklch,var(--chart-2)_40%,transparent)] bg-[color-mix(in_oklch,var(--chart-2)_10%,transparent)] text-foreground [&_svg]:text-[var(--chart-2)]",
				warning:
					"border-[color-mix(in_oklch,var(--chart-4)_45%,transparent)] bg-[color-mix(in_oklch,var(--chart-4)_12%,transparent)] text-foreground [&_svg]:text-[var(--chart-4)]",
				destructive:
					"border-destructive/30 bg-destructive/5 text-foreground [&_svg]:text-destructive",
			},
		},
		defaultVariants: { tone: "info" },
	},
);

const TONE_ICON = {
	info: InfoIcon,
	success: CircleCheckIcon,
	warning: TriangleAlertIcon,
	destructive: CircleAlertIcon,
} as const;

/** An inline advisory strip (a.k.a. callout): a toned surface with a leading
 * icon, a title/body, and optional trailing actions. For a page-level notice or
 * an inline hint — not a transient toast (that is `Sonner`). */
function Banner({
	tone = "info",
	title,
	icon,
	actions,
	children,
	className,
	...props
}: Omit<React.ComponentProps<"div">, "title"> &
	VariantProps<typeof bannerVariants> & {
		title?: React.ReactNode;
		icon?: React.ReactNode;
		actions?: React.ReactNode;
	}) {
	const Icon = TONE_ICON[tone ?? "info"];
	return (
		<div
			data-slot="banner"
			className={cn(bannerVariants({ tone }), className)}
			{...props}
		>
			{icon ?? <Icon aria-hidden />}
			<div className="flex-1 space-y-0.5">
				{title && <p className="font-medium">{title}</p>}
				{children && <div className="text-muted-foreground">{children}</div>}
			</div>
			{actions && (
				<div className="flex shrink-0 items-center gap-2">{actions}</div>
			)}
		</div>
	);
}

export { Banner, bannerVariants };
