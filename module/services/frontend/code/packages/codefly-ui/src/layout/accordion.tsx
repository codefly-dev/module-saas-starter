"use client";

import { Accordion as Primitive } from "@base-ui/react/accordion";
import { ChevronRightIcon } from "lucide-react";
import { cn } from "./cn.js";

export function Accordion<Value>(props: Primitive.Root.Props<Value>) {
	return <Primitive.Root data-slot="accordion" {...props} />;
}
export function AccordionItem({ className, ...props }: Primitive.Item.Props) {
	return (
		<Primitive.Item
			data-slot="accordion-item"
			className={cn("border-b border-border", className)}
			{...props}
		/>
	);
}
export function AccordionHeader(props: Primitive.Header.Props) {
	return <Primitive.Header data-slot="accordion-header" {...props} />;
}
export function AccordionTrigger({
	className,
	children,
	...props
}: Primitive.Trigger.Props) {
	return (
		<Primitive.Trigger
			data-slot="accordion-trigger"
			className={cn(
				"group flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left type-section-title outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50",
				className,
			)}
			{...props}
		>
			<ChevronRightIcon
				aria-hidden
				className="size-4 shrink-0 transition-transform group-data-[panel-open]:rotate-90"
			/>
			{children}
		</Primitive.Trigger>
	);
}
export function AccordionContent({
	className,
	...props
}: Primitive.Panel.Props) {
	return (
		<Primitive.Panel
			data-slot="accordion-content"
			className={cn("px-3 pb-3 type-card-description", className)}
			{...props}
		/>
	);
}
