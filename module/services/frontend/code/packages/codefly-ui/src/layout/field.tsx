"use client";

import type * as React from "react";
import { useId } from "react";

import { cn } from "./cn.js";
import { Label } from "./label.js";

// A labelled form control: the label, the description, the error, and the wiring
// between them. The wiring is the reason this exists — `aria-describedby` and
// `aria-invalid` have to name ids that are generated, so every call site that
// does it by hand does it slightly differently or not at all.
//
// It takes the control as `children` and clones nothing: the caller renders its
// own `Input`/`Select`/`Textarea` and receives the ids through a render prop, so
// Field never has to know which control it is wrapping.

export interface FieldControlProps {
	id: string;
	"aria-describedby": string | undefined;
	"aria-invalid": boolean | undefined;
	required: boolean | undefined;
}

export interface FieldProps
	extends Omit<React.ComponentProps<"div">, "children"> {
	label: React.ReactNode;
	description?: React.ReactNode;
	/** Present means invalid: it sets `aria-invalid` as well as rendering. */
	error?: React.ReactNode;
	required?: boolean;
	children: (control: FieldControlProps) => React.ReactNode;
}

function Field({
	label,
	description,
	error,
	required,
	children,
	className,
	...props
}: FieldProps) {
	const id = useId();
	const controlId = `${id}-control`;
	const descriptionId = `${id}-description`;
	const errorId = `${id}-error`;
	// The error is named first so a screen reader reaches the problem before the
	// hint, and the description stays available rather than being replaced.
	const describedBy =
		[error ? errorId : undefined, description ? descriptionId : undefined]
			.filter(Boolean)
			.join(" ") || undefined;

	return (
		<div
			data-slot="field"
			data-invalid={error ? "true" : undefined}
			className={cn("flex flex-col gap-1.5", className)}
			{...props}
		>
			<Label htmlFor={controlId} data-slot="field-label">
				{label}
				{required && (
					<span aria-hidden="true" className="text-destructive">
						*
					</span>
				)}
			</Label>
			{children({
				id: controlId,
				"aria-describedby": describedBy,
				"aria-invalid": error ? true : undefined,
				required,
			})}
			{description && (
				<p
					id={descriptionId}
					data-slot="field-description"
					className="type-field-description text-muted-foreground"
				>
					{description}
				</p>
			)}
			{error && (
				<p
					id={errorId}
					data-slot="field-error"
					// Announced when it appears: a validation message that renders
					// silently is one a screen-reader user never learns about.
					role="alert"
					className="type-field-error text-destructive"
				>
					{error}
				</p>
			)}
		</div>
	);
}

export { Field };
