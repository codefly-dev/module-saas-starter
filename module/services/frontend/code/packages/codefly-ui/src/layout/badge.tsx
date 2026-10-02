import { mergeProps } from "@base-ui/react/merge-props";
import { useRender } from "@base-ui/react/use-render";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "./cn.js";

const badgeVariants = cva(
	"group/badge inline-flex h-5 w-fit shrink-0 items-center justify-center gap-1 overflow-hidden rounded-4xl border border-transparent px-2 py-0.5 type-badge whitespace-nowrap transition-all focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&>svg]:pointer-events-none [&>svg]:size-3!",
	{
		variants: {
			variant: {
				default: "bg-primary text-primary-foreground [a]:hover:bg-primary/80",
				secondary:
					"bg-secondary text-secondary-foreground [a]:hover:bg-secondary/80",
				destructive:
					"bg-destructive/10 text-destructive focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:focus-visible:ring-destructive/40 [a]:hover:bg-destructive/20",
				outline:
					"border-border text-foreground [a]:hover:bg-muted [a]:hover:text-muted-foreground",
				ghost:
					"hover:bg-muted hover:text-muted-foreground dark:hover:bg-muted/50",
				link: "text-primary underline-offset-4 hover:underline",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	},
);

/**
 * `dot` prefixes a small filled circle that inherits the variant's text colour.
 *
 * It is decorative and `aria-hidden`: the badge's own text is what a reader and
 * a screen reader get, so the dot adds a glanceable mark without adding a
 * second, colour-only channel that carries meaning of its own. That is the
 * point of keeping it here rather than letting each caller prepend its own —
 * a hand-rolled dot is where a status ends up encoded in colour alone.
 */
function Badge({
	className,
	variant = "default",
	dot = false,
	render,
	children,
	...props
}: useRender.ComponentProps<"span"> &
	VariantProps<typeof badgeVariants> & { dot?: boolean }) {
	return useRender({
		defaultTagName: "span",
		props: mergeProps<"span">(
			{
				className: cn(badgeVariants({ variant }), className),
				children: dot ? (
					<>
						<span
							aria-hidden
							data-slot="badge-dot"
							className="size-1.5 shrink-0 rounded-full bg-current"
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
			variant,
		},
	});
}

export { Badge, badgeVariants };
