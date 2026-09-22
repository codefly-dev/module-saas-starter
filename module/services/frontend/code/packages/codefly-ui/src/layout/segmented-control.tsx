"use client";

import { Toggle } from "@base-ui/react/toggle";
import { ToggleGroup } from "@base-ui/react/toggle-group";
import type * as React from "react";

import { cn } from "./cn.js";

// A single-choice control that shows every option at once, sized off the shared
// control rungs so it lines up with a button or an input beside it in a toolbar.
//
// The view switcher some products carry is a USAGE of this — this control at
// `sm` with icon-only options — not a separate component. Splitting them would
// mean two size ladders and two sets of interaction states to keep in step.
//
// Controlled and stateless, so it renders the same on the server and the client.
// `ToggleGroup` is multi-select underneath; single choice is expressed by
// passing one value in and reading the first out, which keeps the roving-focus
// and keyboard behaviour the primitive already implements.

export interface SegmentedControlOption<Value extends string = string> {
	value: Value;
	label: React.ReactNode;
	/** Rendered before the label, or alone when `iconOnly` is set. */
	icon?: React.ReactNode;
	disabled?: boolean;
	/** Required when `iconOnly` is set, since the label is not rendered. */
	"aria-label"?: string;
}

export interface SegmentedControlProps<Value extends string = string>
	extends Omit<
		React.ComponentProps<"div">,
		"onChange" | "defaultValue" | "dir"
	> {
	options: readonly SegmentedControlOption<Value>[];
	value: Value;
	onValueChange: (value: Value) => void;
	/** A shared control rung, so this lines up with a button of the same size. */
	size?: "xs" | "sm" | "default" | "lg";
	/** Show icons alone. Each option then needs an `aria-label`. */
	iconOnly?: boolean;
	disabled?: boolean;
}

// Literal class names, never interpolated: Tailwind extracts classes by scanning
// source text, so a `control-glyph-${size}` built at runtime is a class the
// stylesheet never emits and an icon that silently renders at its intrinsic size.
const containerRungs = {
	xs: "control-height-xs",
	sm: "control-height-sm",
	default: "control-height-default",
	lg: "control-height-lg",
} as const;

const segmentGlyphs = {
	xs: "[&_svg:not([class*='size-'])]:control-glyph-xs",
	sm: "[&_svg:not([class*='size-'])]:control-glyph-sm",
	default: "[&_svg:not([class*='size-'])]:control-glyph-default",
	lg: "[&_svg:not([class*='size-'])]:control-glyph-lg",
} as const;

function SegmentedControl<Value extends string = string>({
	options,
	value,
	onValueChange,
	size = "default",
	iconOnly = false,
	disabled = false,
	className,
	...props
}: SegmentedControlProps<Value>) {
	return (
		<ToggleGroup
			data-slot="segmented-control"
			data-size={size}
			disabled={disabled}
			value={[value]}
			onValueChange={(next) => {
				// Re-pressing the active segment clears the group. A single-choice
				// control has no empty state, so that is ignored rather than
				// reported as a change to nothing.
				const [selected] = next;
				if (selected !== undefined && selected !== value)
					onValueChange(selected as Value);
			}}
			className={cn(
				"group/segmented-control inline-flex w-fit items-center justify-center rounded-lg bg-muted p-[3px] text-muted-foreground",
				containerRungs[size],
				className,
			)}
			{...props}
		>
			{options.map((option) => (
				<Toggle
					key={option.value}
					value={option.value}
					disabled={option.disabled}
					aria-label={option["aria-label"]}
					data-slot="segmented-control-segment"
					className={cn(
						"inline-flex h-full flex-1 items-center justify-center gap-1.5 rounded-md border border-transparent whitespace-nowrap transition-all outline-none select-none",
						"hover:text-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50",
						"disabled:pointer-events-none disabled:opacity-50",
						"data-pressed:bg-background data-pressed:text-foreground data-pressed:shadow-sm",
						"[&_svg]:pointer-events-none [&_svg]:shrink-0",
						"type-segmented-control-segment",
						segmentGlyphs[size],
						iconOnly ? "aspect-square px-0" : "px-2.5",
					)}
				>
					{option.icon}
					{!iconOnly && option.label}
				</Toggle>
			))}
		</ToggleGroup>
	);
}

export { SegmentedControl };
