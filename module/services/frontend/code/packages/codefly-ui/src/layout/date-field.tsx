"use client";

import { useId, useState, useSyncExternalStore } from "react";
import type * as React from "react";

import { cn } from "./cn.js";
import { Fieldset, FieldsetLegend } from "./fieldset.js";
import { Input } from "./input.js";
import { Label } from "./label.js";

// A date entry control with two shapes behind one API.
//
// The two are not a styling choice: they differ in DOM, in the value the user
// is asked for, and in what an error can point at. Which one a product wants
// is a product decision, so it is a `variant` here rather than something the
// skin's token layer could ever express.
//
//   "compact" — one native date input. The browser owns the calendar, the
//   keyboard behaviour and the parsing. Smallest control, fits inline with
//   other fields. Two costs: the picker is browser chrome, so the skin cannot
//   reach inside it, and the displayed order (mm/dd vs dd/mm) comes from the
//   viewer's locale rather than from the product.
//
//   "parts" — day, month and year as separate inputs under one legend. More
//   room, but the order is explicit, whatever the person typed is kept rather
//   than reformatted or discarded, and an error can name the part that is
//   wrong. The controls are ordinary inputs, so the skin governs all of it.
//
// Both carry the same value: an ISO `yyyy-mm-dd` string, or "" when the date
// is incomplete. A caller never has to know which variant it rendered.

export type DateFieldVariant = "compact" | "parts";

/** Which part of a "parts" date an error is about, for targeted messages. */
export type DateFieldPart = "day" | "month" | "year";

export interface DateFieldProps {
	/** The caption for the whole control. */
	label: React.ReactNode;
	/** ISO `yyyy-mm-dd`, or "" when incomplete. */
	value: string;
	onValueChange: (value: string) => void;
	/**
	 * Which shape to render. Left unset, the skin decides through its
	 * `appearance.datePattern`, so one call site renders differently per
	 * deployment. Set it only when a screen must pin one shape regardless.
	 */
	variant?: DateFieldVariant;
	description?: React.ReactNode;
	/** Present means invalid. Sets `aria-invalid` as well as rendering. */
	error?: React.ReactNode;
	/**
	 * Which parts the error is about, so "parts" can mark only those. Ignored
	 * by "compact", which has a single control to mark.
	 */
	errorParts?: readonly DateFieldPart[];
	required?: boolean;
	className?: string;
}

/** Split an ISO date into parts without reformatting or validating them. */
function splitIso(value: string): Record<DateFieldPart, string> {
	const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
	if (!match) return { day: "", month: "", year: "" };
	return { year: match[1], month: match[2], day: match[3] };
}

/**
 * Rebuild an ISO date from parts. Returns "" unless all three are present and
 * in range, so a half-typed date reads as absent rather than as a wrong date.
 * Deliberately not a calendar check: 31 February is rejected by the caller's
 * own validation, which owns the message the person reads.
 */
function joinIso(parts: Record<DateFieldPart, string>): string {
	const { day, month, year } = parts;
	if (!day || !month || !year) return "";
	if (year.length !== 4) return "";
	const d = Number(day);
	const m = Number(month);
	if (!Number.isInteger(d) || d < 1 || d > 31) return "";
	if (!Number.isInteger(m) || m < 1 || m > 12) return "";
	return `${year}-${String(m).padStart(2, "0")}-${String(d).padStart(2, "0")}`;
}

/**
 * The skin's date pattern, read from the `--appearance-date-pattern` custom
 * property the appearance projection sets on <html>. That property is the
 * same channel every token travels, so the explorer and SSR agree without a
 * second context.
 *
 * Subscribed rather than read once: the product fixes it for a page's life,
 * but the explorer swaps skins in place, and a control that ignored the swap
 * would show the wrong shape until a reload. `useSyncExternalStore` also
 * gives SSR its own snapshot, so the server never touches `document`.
 */
function readDatePattern(): DateFieldVariant {
	const raw = getComputedStyle(document.documentElement)
		.getPropertyValue("--appearance-date-pattern")
		.trim();
	return raw === "compact" ? "compact" : "parts";
}

function subscribeDatePattern(onChange: () => void): () => void {
	// The projection writes inline `style` on <html>; watch that attribute.
	const observer = new MutationObserver(onChange);
	observer.observe(document.documentElement, {
		attributes: true,
		attributeFilter: ["style"],
	});
	return () => observer.disconnect();
}

function useSkinDatePattern(): DateFieldVariant {
	return useSyncExternalStore(
		subscribeDatePattern,
		readDatePattern,
		() => "parts",
	);
}

function DateField({
	label,
	value,
	onValueChange,
	variant: variantProp,
	description,
	error,
	errorParts,
	required,
	className,
}: DateFieldProps) {
	const skinVariant = useSkinDatePattern();
	const variant = variantProp ?? skinVariant;
	const id = useId();
	const descriptionId = `${id}-description`;
	const errorId = `${id}-error`;
	// The error is named before the hint so a screen reader reaches the problem
	// first, and the description stays available rather than being replaced —
	// the same ordering `Field` uses.
	const describedBy =
		[error ? errorId : undefined, description ? descriptionId : undefined]
			.filter(Boolean)
			.join(" ") || undefined;

	// The parts are held here, not derived from `value`, and that is the point
	// of this variant: an incomplete date has no ISO representation, so deriving
	// the boxes from the ISO string would blank out what the person had typed
	// the moment it stopped being a whole date. They keep their digits; `value`
	// simply reads "" until all three make a date.
	const [parts, setParts] = useState(() => splitIso(value));
	const [lastValue, setLastValue] = useState(value);
	// Re-sync when the value changes from outside (a reset, a loaded record).
	// Adjusting state during render rather than in an effect: an effect would
	// paint the stale parts first, and this kit treats set-state-in-effect as
	// an error.
	if (value !== lastValue) {
		setLastValue(value);
		if (value !== joinIso(parts)) setParts(splitIso(value));
	}

	function setPart(part: DateFieldPart, next: string) {
		// Digits only, and never longer than the part can hold. Anything else is
		// dropped as it is typed rather than accepted and rejected on submit.
		const digits = next.replace(/\D/g, "").slice(0, part === "year" ? 4 : 2);
		const nextParts = { ...parts, [part]: digits };
		setParts(nextParts);
		setLastValue(joinIso(nextParts));
		onValueChange(joinIso(nextParts));
	}

	const body =
		variant === "compact" ? (
			<Input
				id={`${id}-control`}
				type="date"
				value={value}
				onChange={(event) => onValueChange(event.target.value)}
				aria-describedby={describedBy}
				aria-invalid={error ? true : undefined}
				required={required}
			/>
		) : (
			<div data-slot="date-field-parts" className="flex gap-2">
				{(["day", "month", "year"] as const).map((part) => (
					<div key={part} className="flex flex-col gap-1">
						<Label htmlFor={`${id}-${part}`} data-slot="date-field-part-label">
							{part === "day" ? "Day" : part === "month" ? "Month" : "Year"}
						</Label>
						<Input
							id={`${id}-${part}`}
							// `inputMode` rather than `type="number"`: a spinner invites
							// arrow-key nudging of a value that was typed deliberately,
							// and a number input silently discards a leading zero.
							inputMode="numeric"
							autoComplete="off"
							value={parts[part]}
							onChange={(event) => setPart(part, event.target.value)}
							aria-describedby={describedBy}
							aria-invalid={
								error && (!errorParts || errorParts.includes(part))
									? true
									: undefined
							}
							required={required}
							className={cn(part === "year" ? "w-16" : "w-12")}
						/>
					</div>
				))}
			</div>
		);

	if (variant === "compact") {
		return (
			<div
				data-slot="date-field"
				data-variant="compact"
				data-invalid={error ? "true" : undefined}
				className={cn("flex flex-col gap-1.5", className)}
			>
				<Label htmlFor={`${id}-control`} data-slot="date-field-label">
					{label}
					{required && (
						<span aria-hidden="true" className="text-destructive">
							*
						</span>
					)}
				</Label>
				{body}
				<DateFieldMessages
					description={description}
					descriptionId={descriptionId}
					error={error}
					errorId={errorId}
				/>
			</div>
		);
	}

	return (
		<Fieldset
			data-slot="date-field"
			data-variant="parts"
			data-invalid={error ? "true" : undefined}
			className={className}
		>
			<FieldsetLegend data-slot="date-field-label">
				{label}
				{required && (
					<span aria-hidden="true" className="text-destructive">
						*
					</span>
				)}
			</FieldsetLegend>
			{body}
			<DateFieldMessages
				description={description}
				descriptionId={descriptionId}
				error={error}
				errorId={errorId}
			/>
		</Fieldset>
	);
}

function DateFieldMessages({
	description,
	descriptionId,
	error,
	errorId,
}: {
	description: React.ReactNode;
	descriptionId: string;
	error: React.ReactNode;
	errorId: string;
}) {
	return (
		<>
			{description && (
				<p
					id={descriptionId}
					data-slot="date-field-description"
					className="type-field-description text-muted-foreground"
				>
					{description}
				</p>
			)}
			{error && (
				<p
					id={errorId}
					data-slot="date-field-error"
					// Announced when it appears: a validation message that renders
					// silently is one a screen-reader user never learns about.
					role="alert"
					className="type-field-error text-destructive"
				>
					{error}
				</p>
			)}
		</>
	);
}

export { DateField };
