"use client";

import { Radio as RadioPrimitive } from "@base-ui/react/radio";
import { RadioGroup as RadioGroupPrimitive } from "@base-ui/react/radio-group";
import type * as React from "react";
import { cn } from "./cn.js";

/** A set of mutually exclusive options. Controlled or uncontrolled via Base UI's
 * `value` / `defaultValue`. Compose `RadioGroupItem` (+ a `Label`) per option. */
function RadioGroup({
	className,
	...props
}: React.ComponentProps<typeof RadioGroupPrimitive>) {
	return (
		<RadioGroupPrimitive
			data-slot="radio-group"
			className={cn("grid gap-2", className)}
			{...props}
		/>
	);
}

function RadioGroupItem({
	className,
	...props
}: React.ComponentProps<typeof RadioPrimitive.Root>) {
	return (
		<RadioPrimitive.Root
			data-slot="radio-group-item"
			className={cn(
				"flex size-4 shrink-0 items-center justify-center rounded-full border border-input bg-transparent text-primary shadow-sm outline-none transition-colors focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 data-[checked]:border-primary disabled:cursor-not-allowed disabled:opacity-50",
				className,
			)}
			{...props}
		>
			<RadioPrimitive.Indicator className="flex data-[unchecked]:hidden">
				<span className="size-2 rounded-full bg-primary" />
			</RadioPrimitive.Indicator>
		</RadioPrimitive.Root>
	);
}

export { RadioGroup, RadioGroupItem };
