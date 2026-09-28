"use client";

import { type ReactNode, useEffect, useRef, useState } from "react";
import { cn } from "./cn.js";

/**
 * How long a wait must last before anything is shown for it.
 *
 * Most waits end inside this window, and an indicator that appears and vanishes
 * inside it reads as a flicker rather than as information — it tells the reader
 * nothing they could not already see, while drawing their eye away from what
 * they were reading.
 */
export const LOADING_DELAY_MS = 200;

/**
 * How long an indicator stays once it has appeared.
 *
 * Without a floor, a wait that ends just past the delay shows the indicator for
 * a few milliseconds — the exact blink the delay exists to prevent, moved
 * later rather than removed. The floor is what makes an indicator that appears
 * at all appear for long enough to be read.
 */
export const LOADING_MIN_VISIBLE_MS = 300;

export interface DelayedLoadingOptions {
	/** Overrides `LOADING_DELAY_MS`. */
	delayMs?: number;
	/** Overrides `LOADING_MIN_VISIBLE_MS`. */
	minVisibleMs?: number;
}

/**
 * Whether to show a loading indicator for a wait that is `active`.
 *
 * Two rules, and they compose into one promise: **a loading indicator never
 * flashes.** A wait shorter than the delay shows nothing at all; a wait that
 * crosses it shows an indicator that stays put long enough to read, even if the
 * answer lands immediately afterwards.
 *
 * The clock is read in effects and timers, never during render, so two renders
 * of the same state can never disagree about what is on screen.
 */
export function useDelayedLoading(
	active: boolean,
	{
		delayMs = LOADING_DELAY_MS,
		minVisibleMs = LOADING_MIN_VISIBLE_MS,
	}: DelayedLoadingOptions = {},
): boolean {
	const [visible, setVisible] = useState(false);
	// When the indicator went up, so the floor is measured from the moment it
	// became visible rather than from the moment the wait started.
	const shownAt = useRef(0);

	useEffect(() => {
		if (active) {
			// Already up: keep it up. A wait that restarts while the indicator is
			// showing must not restart the delay, or a series of quick refetches
			// would tear it down and build it up again.
			if (visible) return;
			const timer = setTimeout(() => {
				shownAt.current = Date.now();
				setVisible(true);
			}, delayMs);
			return () => clearTimeout(timer);
		}
		if (!visible) return;
		const remaining = minVisibleMs - (Date.now() - shownAt.current);
		if (remaining <= 0) {
			setVisible(false);
			return;
		}
		const timer = setTimeout(() => setVisible(false), remaining);
		return () => clearTimeout(timer);
	}, [active, visible, delayMs, minVisibleMs]);

	return visible;
}

export interface SpinnerProps {
	/**
	 * Required: what is being waited for. A spinner with no accessible name
	 * announces that *something* is happening, which is the one thing the reader
	 * can already see.
	 */
	label: string;
	size?: "sm" | "md";
	className?: string;
}

/**
 * The kit's busy indicator.
 *
 * `role="status"` with a polite live region: a wait is information the reader
 * may want, never an interruption. The ring itself is `aria-hidden`; the label
 * is what assistive technology reads.
 *
 * Prefer `DelayedLoading` over rendering this directly — on its own it has no
 * delay, so it will flash on a fast response.
 */
export function Spinner({ label, size = "md", className }: SpinnerProps) {
	return (
		<span
			data-slot="spinner"
			role="status"
			// `status` is not a name-from-content role, so the label has to be an
			// aria-label: without it the element is announced with no name at all.
			// The visually-hidden copy below stays, so the live region has text to
			// announce when it appears.
			aria-label={label}
			aria-live="polite"
			aria-busy="true"
			className={cn("inline-flex items-center gap-2", className)}
		>
			<span
				aria-hidden="true"
				className={cn(
					"inline-block rounded-full border-2 border-current border-t-transparent",
					// Rotation is vestibular-triggering, so a reader who asked for
					// reduced motion gets a fade instead of a spin rather than a ring
					// that sits still and conveys nothing. Both still say "busy"; only
					// one of them moves through space.
					"motion-safe:animate-spin motion-reduce:animate-pulse",
					size === "sm" ? "size-3" : "size-4",
				)}
			/>
			<span className="sr-only">{label}</span>
		</span>
	);
}

export interface DelayedLoadingProps extends DelayedLoadingOptions {
	/** Whether the thing being waited for is still outstanding. */
	active: boolean;
	/** The indicator's accessible name, and the default spinner's label. */
	label: string;
	/**
	 * What to render while the indicator is up. Omitted, the kit's `Spinner` is
	 * used — which is the point of the primitive: a caller gets the timing
	 * without having to own it.
	 */
	children?: ReactNode;
	className?: string;
}

/**
 * Renders a loading indicator only once a wait has earned one, and then for
 * long enough to read.
 *
 * The product rule this exists to make unwritable-otherwise: **never flash a
 * loading indicator.** Nothing appears before `delayMs`; once something has
 * appeared it stays for at least `minVisibleMs`; a fast response shows nothing
 * at all.
 */
export function DelayedLoading({
	active,
	label,
	children,
	className,
	delayMs,
	minVisibleMs,
}: DelayedLoadingProps) {
	const visible = useDelayedLoading(active, {
		...(delayMs === undefined ? {} : { delayMs }),
		...(minVisibleMs === undefined ? {} : { minVisibleMs }),
	});
	if (!visible) return null;
	return children ? (
		<div data-slot="delayed-loading" className={className}>
			{children}
		</div>
	) : (
		<Spinner label={label} className={className} />
	);
}
