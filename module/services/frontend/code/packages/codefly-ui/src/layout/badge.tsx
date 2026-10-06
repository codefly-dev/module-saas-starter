import { mergeProps } from "@base-ui/react/merge-props";
import { useRender } from "@base-ui/react/use-render";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "./cn.js";

/**
 * What a status says, as opposed to how loudly a badge says it (`variant`).
 * `danger` reads the `destructive` token; the others read their own. Shared by
 * every kit component that carries a status, so a page's Connected badge, its
 * owner chip and its warning banner agree on what green means.
 */
export type StatusTone = "neutral" | "success" | "warning" | "danger" | "info";

const badgeVariants = cva(
	"group/badge inline-flex h-5 w-fit shrink-0 items-center justify-center gap-1 overflow-hidden rounded-4xl border border-transparent px-2 py-0.5 type-badge whitespace-nowrap transition-all focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&>svg]:pointer-events-none [&>svg]:size-3!",
	{
		variants: {
			variant: {
				default: "bg-primary text-primary-foreground [a]:hover:bg-primary/80",
				secondary:
					"bg-secondary text-secondary-foreground [a]:hover:bg-secondary/80",
				destructive:
					"bg-destructive/10 [color:color-mix(in_oklab,var(--destructive)_70%,var(--foreground))] focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:focus-visible:ring-destructive/40 [a]:hover:bg-destructive/20",
				outline:
					"border-border text-foreground [a]:hover:bg-muted [a]:hover:text-muted-foreground",
				ghost:
					"hover:bg-muted hover:text-muted-foreground dark:hover:bg-muted/50",
				link: "text-primary underline-offset-4 hover:underline",
			},
			// Danger text mixes toward foreground to retain small-text contrast
			// on its own tint in both modes. A tint of the tone's own colour, the shape
			// `destructive` already had, so every tone reads at the same weight
			// and none of them is mistaken for the primary action.
			tone: {
				neutral: "bg-muted text-muted-foreground [a]:hover:bg-muted/80",
				success:
					"bg-success/10 text-success dark:bg-success/20 [a]:hover:bg-success/20",
				warning:
					"bg-warning/10 text-warning dark:bg-warning/20 [a]:hover:bg-warning/20",
				danger:
					"bg-destructive/10 [color:color-mix(in_oklab,var(--destructive)_70%,var(--foreground))] dark:bg-destructive/20 [a]:hover:bg-destructive/20",
				info: "bg-info/10 text-info dark:bg-info/20 [a]:hover:bg-info/20",
			},
			size: {
				sm: "h-4 px-1.5 py-0",
				default: "",
				lg: "h-6 gap-1.5 px-2.5 type-badge-lg [&>svg]:size-3.5!",
			},
		},
		defaultVariants: {
			variant: "default",
			size: "default",
		},
	},
);

const dotSize = { sm: "size-1", default: "size-1.5", lg: "size-2" } as const;

function Badge({
	className,
	variant = "default",
	tone,
	size = "default",
	dot = false,
	render,
	children,
	...props
}: useRender.ComponentProps<"span"> &
	VariantProps<typeof badgeVariants> & { dot?: boolean }) {
	// A tone replaces the variant's colours rather than layering over them: two
	// fills on one badge resolve by class order, which is not a decision.
	const resolvedSize = size ?? "default";
	return useRender({
		defaultTagName: "span",
		props: mergeProps<"span">(
			{
				className: cn(
					badgeVariants({
						variant: tone ? null : variant,
						tone,
						size: resolvedSize,
					}),
					className,
				),
				// Decorative: the text is what a reader and a screen reader get. The
				// dot gives a row of badges a shape to tell apart, so the status is
				// never carried by colour alone.
				children: dot ? (
					<>
						<span
							aria-hidden
							data-slot="badge-dot"
							className={cn(
								"shrink-0 rounded-full bg-current",
								dotSize[resolvedSize],
							)}
						/>
						{children}
					</>
				) : (
					children
				),
			},
			props,
		),
		render,
		state: {
			slot: "badge",
			variant: tone ? undefined : variant,
			tone,
			size: resolvedSize,
		},
	});
}

export { Badge, badgeVariants };
