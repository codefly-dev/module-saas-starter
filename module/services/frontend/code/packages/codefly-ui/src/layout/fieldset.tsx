"use client";

import { Fieldset as FieldsetPrimitive } from "@base-ui/react/fieldset";
import type * as React from "react";

import { cn } from "./cn.js";

// A group of controls under one caption.
//
// `Field` covers the common case — one label naming one control, wired by
// `htmlFor`. That wiring cannot name more than one control, so a group of
// related inputs (a date split into day/month/year, an address, a pair of
// range bounds) needs a `<fieldset>` and a `<legend>` instead: the legend
// captions the whole group, and each control keeps its own label.

function Fieldset({
	className,
	...props
}: React.ComponentProps<typeof FieldsetPrimitive.Root>) {
	return (
		<FieldsetPrimitive.Root
			data-slot="fieldset"
			className={cn("flex flex-col gap-1.5 border-0 p-0", className)}
			{...props}
		/>
	);
}

function FieldsetLegend({
	className,
	...props
}: React.ComponentProps<typeof FieldsetPrimitive.Legend>) {
	return (
		<FieldsetPrimitive.Legend
			data-slot="fieldset-legend"
			className={cn("type-field-label text-foreground", className)}
			{...props}
		/>
	);
}

export { Fieldset, FieldsetLegend };
