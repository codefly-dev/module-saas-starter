import { cn } from "./cn.js";

export interface ProgressProps {
	/** 0–100. Clamped, so a caller's arithmetic cannot overflow the track. */
	value: number;
	/**
	 * Required: the bar's accessible name. A progress bar with no name announces
	 * only a percentage, which tells a screen-reader user how far along
	 * *something* is.
	 */
	label: string;
	/**
	 * What the percentage means in words — "Fetching from the repository" — read
	 * instead of the bare number by assistive technology, which is almost always
	 * the more useful of the two.
	 */
	valueText?: string;
	/**
	 * How the fill reads. `neutral` follows the accent; `success`, `warning` and
	 * `danger` carry the status colours, so a stalled or failed bar is
	 * distinguishable from a healthy one without reading its label.
	 */
	tone?: "neutral" | "success" | "warning" | "danger";
	/** Divides the track into equal segments: a phase ladder rather than a smooth fill. */
	steps?: number;
	className?: string;
}

const toneFill: Record<NonNullable<ProgressProps["tone"]>, string> = {
	neutral: "bg-primary",
	success: "bg-primary",
	warning: "bg-amber-500 dark:bg-amber-400",
	danger: "bg-destructive",
};

/** Where the dividers fall, as fractions of the track. */
function stepBoundaries(steps: number): number[] {
	const boundaries: number[] = [];
	for (let step = 1; step < steps; step += 1) boundaries.push(step / steps);
	return boundaries;
}

/**
 * A determinate progress bar: the caller owns the number, this owns how it
 * reads.
 *
 * Determinate only. An indeterminate bar promises that something is happening
 * while saying nothing about what, and every caller here has a real phase to
 * name — so the honest rendering of "we do not know" is a label, not an
 * animation.
 */
export function Progress({
	value,
	label,
	valueText,
	tone = "neutral",
	steps,
	className,
}: ProgressProps) {
	const percent = Math.min(100, Math.max(0, Math.round(value)));
	return (
		<div
			data-slot="progress"
			role="progressbar"
			aria-label={label}
			aria-valuenow={percent}
			aria-valuemin={0}
			aria-valuemax={100}
			{...(valueText ? { "aria-valuetext": valueText } : {})}
			className={cn(
				"relative h-2 w-full overflow-hidden rounded-full bg-muted",
				className,
			)}
		>
			<div
				data-slot="progress-fill"
				className={cn(
					"h-full rounded-full transition-[width] duration-500 ease-out",
					toneFill[tone],
				)}
				style={{ width: `${percent}%` }}
			/>
			{/* The dividers sit above the fill so a part-filled segment still reads
			    as one segment rather than as a smooth bar that happens to stop. */}
			{steps && steps > 1 && (
				<div
					aria-hidden="true"
					className="absolute inset-0"
					data-slot="progress-steps"
				>
					{/* Keyed by where the divider sits rather than by its place in the
					    array: the boundary between step 2 and step 3 is the same divider
					    whatever the ladder's length. */}
					{stepBoundaries(steps).map((fraction) => (
						<div
							key={fraction}
							className="absolute top-0 h-full border-background border-r-2"
							style={{ left: `${fraction * 100}%` }}
						/>
					))}
				</div>
			)}
		</div>
	);
}
