"use client";

import { Radio as Primitive } from "@base-ui/react/radio";
import { RadioGroup as Group } from "@base-ui/react/radio-group";
import { cn } from "./cn.js";

/** Base UI owns group focus, form values, disabled and validation behavior. */
export function RadioGroup<Value>({ className, ...props }: Group.Props<Value>) {
	return (
		<Group
			data-slot="radio-group"
			className={cn("grid gap-2 type-label", className)}
			{...props}
		/>
	);
}
export function Radio({ className, ...props }: Primitive.Root.Props) {
	return (
		<Primitive.Root
			data-slot="radio"
			className={cn(
				"relative inline-flex size-4 shrink-0 items-center justify-center rounded-full border border-input outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive data-checked:border-primary data-checked:text-primary",
				className,
			)}
			{...props}
		>
			<Primitive.Indicator className="size-2 rounded-full bg-current" />
		</Primitive.Root>
	);
}
