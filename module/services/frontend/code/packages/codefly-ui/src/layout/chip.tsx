import { useRender } from "@base-ui/react/use-render";
import { XIcon } from "lucide-react";
import {
	Children,
	isValidElement,
	type MouseEventHandler,
	type ReactElement,
	type ReactNode,
} from "react";

import type { StatusTone } from "./badge.js";
import { cn } from "./cn.js";

const toneClasses: Record<StatusTone, string> = {
	neutral: "border-border bg-background text-foreground",
	success: "border-transparent bg-success/10 text-success dark:bg-success/20",
	warning: "border-transparent bg-warning/10 text-warning dark:bg-warning/20",
	danger:
		"border-transparent bg-destructive/10 text-destructive dark:bg-destructive/20",
	info: "border-transparent bg-info/10 text-info dark:bg-info/20",
};

type ChipRemove =
	| { onRemove?: undefined; removeLabel?: undefined }
	// The remove control has no text of its own, and "Remove" alone does not
	// say which of a row of chips it removes.
	| { onRemove: () => void; removeLabel: string };

export type ChipProps = {
	/** The label. */
	children: ReactNode;
	/** Renders the chip as a link. */
	href?: string;
	/** Renders the chip as a button. */
	onClick?: MouseEventHandler<HTMLElement>;
	/** Replaces the link or button, e.g. with a router link. */
	render?: ReactElement;
	icon?: ReactNode;
	/** Trailing, quieter text after a separator: "unread", a count, an owner. */
	meta?: ReactNode;
	tone?: StatusTone;
	className?: string;
} & ChipRemove;

/**
 * A badge a person can act on: follow, press, or remove. The remove control is
 * a sibling of the action rather than inside it, because a button inside a
 * link is two targets that assistive technology announces as one.
 */
export function Chip({
	children,
	href,
	onClick,
	render,
	icon,
	meta,
	tone = "neutral",
	className,
	onRemove,
	removeLabel,
}: ChipProps) {
	const interactive = render !== undefined || href !== undefined || !!onClick;
	const action = useRender({
		defaultTagName: href !== undefined ? "a" : onClick ? "button" : "span",
		render,
		props: {
			href,
			onClick,
			...(href === undefined && onClick && !render
				? { type: "button" as const }
				: {}),
			"data-slot": "chip-action",
			className: cn(
				"inline-flex h-full min-w-0 items-center gap-1 rounded-4xl px-2.5 outline-none [&_svg]:size-3.5 [&_svg]:shrink-0",
				interactive &&
					"cursor-pointer transition-colors hover:bg-foreground/5 focus-visible:ring-[3px] focus-visible:ring-ring/50",
				onRemove && "pr-1",
			),
			children: (
				<>
					{icon && (
						<span aria-hidden data-slot="chip-icon" className="inline-flex">
							{icon}
						</span>
					)}
					<span className="truncate">{children}</span>
					{meta != null && (
						<span
							data-slot="chip-meta"
							className="type-chip-meta opacity-75"
						>
							<span aria-hidden>· </span>
							{meta}
						</span>
					)}
				</>
			),
		},
	});
	return (
		<span
			data-slot="chip"
			data-tone={tone}
			className={cn(
				// No `overflow-hidden`: it would crop the focus ring the action and
				// the remove control draw outside themselves. The label truncates on
				// its own, through `min-w-0` on the action.
				"inline-flex h-6 max-w-full shrink-0 items-center rounded-4xl border type-chip whitespace-nowrap",
				toneClasses[tone],
				className,
			)}
		>
			{action}
			{onRemove && (
				<button
					type="button"
					data-slot="chip-remove"
					aria-label={removeLabel}
					onClick={onRemove}
					className="mr-1 inline-flex size-4 shrink-0 cursor-pointer items-center justify-center rounded-full opacity-75 outline-none hover:bg-foreground/10 hover:opacity-100 focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-3"
				>
					<XIcon />
				</button>
			)}
		</span>
	);
}

export interface ChipGroupProps {
	children: ReactNode;
	/** Names the set ("Affects", "Owners") for a screen reader. */
	label?: string;
	className?: string;
}

/** A wrapping row of chips, announced as a list of them. */
export function ChipGroup({ children, label, className }: ChipGroupProps) {
	return (
		// `role="list"`: Safari drops list semantics from an unstyled `ul`.
		<ul
			role="list"
			data-slot="chip-group"
			aria-label={label}
			className={cn("flex flex-wrap items-center gap-2", className)}
		>
			{Children.toArray(children).map((child, index) => (
				<li
					key={isValidElement(child) && child.key != null ? child.key : index}
					className="flex max-w-full"
				>
					{child}
				</li>
			))}
		</ul>
	);
}
