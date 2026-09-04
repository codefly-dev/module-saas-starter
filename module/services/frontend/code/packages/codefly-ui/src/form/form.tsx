import type * as React from "react";
import { cn } from "../layout/cn.js";
import { Label } from "../layout/label.js";

/** A vertically-spaced form. Presentational only — validation and submission
 * live in the caller; these primitives give a consistent field shape (label,
 * control, description, error) every form composes. */
function Form({ className, ...props }: React.ComponentProps<"form">) {
	return (
		<form data-slot="form" className={cn("space-y-5", className)} {...props} />
	);
}

/** One field: a label/control/description/error stack. */
function FormField({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="form-field"
			className={cn("space-y-1.5", className)}
			{...props}
		/>
	);
}

function FormLabel({
	className,
	...props
}: React.ComponentProps<typeof Label>) {
	return <Label data-slot="form-label" className={className} {...props} />;
}

function FormDescription({ className, ...props }: React.ComponentProps<"p">) {
	return (
		<p
			data-slot="form-description"
			className={cn("text-sm text-muted-foreground", className)}
			{...props}
		/>
	);
}

/** The validation message slot. Renders nothing when it has no children, so a
 * caller can bind it to an error string unconditionally. */
function FormMessage({
	className,
	children,
	...props
}: React.ComponentProps<"p">) {
	if (!children) return null;
	return (
		<p
			data-slot="form-message"
			className={cn("text-sm font-medium text-destructive", className)}
			{...props}
		>
			{children}
		</p>
	);
}

export { Form, FormDescription, FormField, FormLabel, FormMessage };
