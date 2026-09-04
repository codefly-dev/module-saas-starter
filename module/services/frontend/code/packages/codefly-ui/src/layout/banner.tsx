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
			// Status color comes only from tokens the skin owns for status —
			// `--primary` (affirmative accent), `--destructive`, and the neutral
			// muted/border set. The token vocabulary has no success-green or
			// warning-amber, so `warning` is a neutral surface distinguished by its
			// icon rather than a borrowed chart-palette color (which is grayscale by
			// default and re-skins unpredictably).
			tone: {
				info: "border-border bg-muted/50 text-foreground [&_svg]:text-muted-foreground",
				success:
					"border-primary/30 bg-primary/5 text-foreground [&_svg]:text-primary",
				warning:
					"border-border bg-muted/50 text-foreground [&_svg]:text-foreground",
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
			// Announce urgent tones assertively, advisory ones politely; a caller
			// can override via `role`/`aria-live` since {...props} is spread last.
			role={tone === "destructive" || tone === "warning" ? "alert" : "status"}
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
