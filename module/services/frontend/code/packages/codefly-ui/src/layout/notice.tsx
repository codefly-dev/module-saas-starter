"use client";

import { X } from "lucide-react";
import { type ReactNode, useId } from "react";
import { Button } from "./button.js";
import { cn } from "./cn.js";

/**
 * The way out of a `Notice`. The kit renders it — always, as a real, enabled
 * `<button>` in the notice's tab order — so a caller cannot hide it, disable it,
 * or hand in something that is not focusable.
 */
export interface NoticeEscape {
	/** The control's accessible name, and its visible text for `"button"`. */
	label: string;
	onSelect: () => void | Promise<void>;
	/**
	 * `"button"` (the default) puts a labelled button first among the actions —
	 * for an escape that does something, like signing out. `"dismiss"` is an
	 * icon-only close control in the corner — for an escape that only puts the
	 * notice away for now.
	 */
	presentation?: "button" | "dismiss";
}

export interface NoticeProps {
	title: ReactNode;
	children?: ReactNode;
	/** Decorative; rendered beside the title and hidden from assistive tech. */
	icon?: ReactNode;
	/** The notice's own decisions, rendered after the escape. */
	actions?: ReactNode;
	/** Required: a notice that asks for a decision always offers a way out. */
	escape: NoticeEscape;
	/**
	 * `"floating"` (the default) pins the notice to the bottom-start corner above
	 * the page; `"inline"` leaves it in flow.
	 */
	placement?: "floating" | "inline";
	className?: string;
}

/**
 * A persistent, non-modal notice that asks for a decision — accepting updated
 * terms, choosing tracking preferences — while the rest of the page stays usable.
 *
 * It differs from `Banner` (polite status, dismissal optional) in that it
 * floats over the page and can therefore cover what is beneath it, including the
 * account menu. So it cannot be written without an `escape`: the prop is
 * required, and the kit always renders it. There is deliberately no way to pass
 * a disabled one — when the decision itself is unavailable (its content is not
 * configured, a request failed), the escape is what keeps the user from being
 * trapped behind the notice.
 */
export function Notice({
	title,
	children,
	icon,
	actions,
	escape: escapeAffordance,
	placement = "floating",
	className,
}: NoticeProps) {
	const titleId = useId();
	const bodyId = useId();
	const dismiss = escapeAffordance.presentation === "dismiss";
	const escapeControl = dismiss ? (
		<Button
			data-slot="notice-escape"
			variant="ghost"
			size="icon-sm"
			className="shrink-0 text-muted-foreground"
			aria-label={escapeAffordance.label}
			onClick={() => void escapeAffordance.onSelect()}
		>
			<X />
		</Button>
	) : (
		<Button
			data-slot="notice-escape"
			variant="outline"
			onClick={() => void escapeAffordance.onSelect()}
		>
			{escapeAffordance.label}
		</Button>
	);

	return (
		<div
			data-slot="notice"
			data-placement={placement}
			role="dialog"
			aria-modal="false"
			aria-labelledby={titleId}
			aria-describedby={children ? bodyId : undefined}
			className={cn(
				"rounded-2xl border bg-card p-5 type-dialog-content text-card-foreground shadow-2xl",
				placement === "floating" &&
					"fixed right-4 bottom-4 left-4 z-50 mr-auto max-w-lg",
				className,
			)}
		>
			<div className="flex items-start gap-3">
				{icon && (
					<div
						aria-hidden="true"
						className="rounded-lg bg-primary/10 p-2 text-primary [&_svg:not([class*='size-'])]:size-4"
					>
						{icon}
					</div>
				)}
				<div className="min-w-0 flex-1">
					<h2 id={titleId} className="type-dialog-title">
						{title}
					</h2>
					{children && (
						<div
							id={bodyId}
							className="mt-1 type-dialog-description text-muted-foreground"
						>
							{children}
						</div>
					)}
				</div>
				{dismiss && escapeControl}
			</div>
			{(actions || !dismiss) && (
				<div
					data-slot="notice-actions"
					className="mt-4 flex flex-wrap justify-end gap-2"
				>
					{!dismiss && escapeControl}
					{actions}
				</div>
			)}
		</div>
	);
}
