"use client";

import { Autocomplete as CommandPrimitive } from "@base-ui/react/autocomplete";
import { SearchIcon } from "lucide-react";
import type * as React from "react";
import { cn } from "./cn.js";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
} from "./dialog.js";
import { InputGroup, InputGroupAddon } from "./input-group.js";

// Built on Base UI's Autocomplete, the primitive its docs point to for search
// widgets (Combobox does not accept free-form text, Select renders no input).
//
// `inline open` is what makes it usable here: it renders the list without Base
// UI's own popup and positioner, because the palette already supplies its own
// surface — either CommandDialog or a host-owned Dialog.
//
// `mode="none"` leaves the items static and hands filtering to the caller.
// Base UI's built-in filter only applies to an `items` array passed to the
// root, which would force every caller onto render props; keeping the
// compositional children API matters more, and callers like a command palette
// are already filtering server-side for part of their list anyway.
// `CommandPrimitive.useFilter` is re-exported below as `useCommandFilter` so
// callers get the same matcher Base UI would have used.

function Command({
	className,
	value,
	onValueChange,
	children,
	...props
}: Omit<
	React.ComponentProps<typeof CommandPrimitive.Root>,
	"inline" | "open" | "mode" | "children"
> & {
	className?: string;
	children: React.ReactNode;
}) {
	return (
		<CommandPrimitive.Root
			inline
			open
			mode="none"
			value={value}
			onValueChange={onValueChange}
			{...props}
		>
			<div
				data-slot="command"
				className={cn(
					"flex size-full flex-col overflow-hidden rounded-xl! bg-popover p-1 text-popover-foreground",
					className,
				)}
			>
				{children}
			</div>
		</CommandPrimitive.Root>
	);
}

function CommandDialog({
	title = "Command Palette",
	description = "Search for a command to run...",
	children,
	className,
	showCloseButton = false,
	...props
}: Omit<React.ComponentProps<typeof Dialog>, "children"> & {
	title?: string;
	description?: string;
	className?: string;
	showCloseButton?: boolean;
	children: React.ReactNode;
}) {
	return (
		<Dialog {...props}>
			<DialogHeader className="sr-only">
				<DialogTitle>{title}</DialogTitle>
				<DialogDescription>{description}</DialogDescription>
			</DialogHeader>
			<DialogContent
				className={cn(
					"top-1/3 translate-y-0 overflow-hidden rounded-xl! p-0",
					className,
				)}
				showCloseButton={showCloseButton}
			>
				{children}
			</DialogContent>
		</Dialog>
	);
}

function CommandInput({
	className,
	...props
}: React.ComponentProps<typeof CommandPrimitive.Input>) {
	return (
		<div data-slot="command-input-wrapper" className="p-1 pb-0">
			<InputGroup className="h-8! rounded-lg! border-input/30 bg-input/30 shadow-none! *:data-[slot=input-group-addon]:pl-2!">
				<CommandPrimitive.Input
					data-slot="command-input"
					className={cn(
						"w-full type-command-input outline-hidden disabled:cursor-not-allowed disabled:opacity-50",
						className,
					)}
					{...props}
				/>
				<InputGroupAddon>
					<SearchIcon className="size-4 shrink-0 opacity-50" />
				</InputGroupAddon>
			</InputGroup>
		</div>
	);
}

function CommandList({
	className,
	...props
}: React.ComponentProps<typeof CommandPrimitive.List>) {
	return (
		<CommandPrimitive.List
			data-slot="command-list"
			className={cn(
				"no-scrollbar max-h-72 scroll-py-1 overflow-x-hidden overflow-y-auto outline-none",
				className,
			)}
			{...props}
		/>
	);
}

// Base UI's own Empty only knows the list is empty when the root is filtering
// its own `items`. Under `mode="none"` the caller owns that decision, so this
// renders whenever it is mounted — mount it conditionally.
function CommandEmpty({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="command-empty"
			className={cn("py-6 text-center type-command-empty", className)}
			{...props}
		/>
	);
}

function CommandGroup({
	className,
	heading,
	children,
	...props
}: Omit<React.ComponentProps<typeof CommandPrimitive.Group>, "children"> & {
	heading?: React.ReactNode;
	children: React.ReactNode;
}) {
	return (
		<CommandPrimitive.Group
			data-slot="command-group"
			className={cn("overflow-hidden p-1 text-foreground", className)}
			{...props}
		>
			{heading ? (
				<CommandPrimitive.GroupLabel
					data-slot="command-group-heading"
					className="px-2 py-1.5 type-command-group text-muted-foreground"
				>
					{heading}
				</CommandPrimitive.GroupLabel>
			) : null}
			{children}
		</CommandPrimitive.Group>
	);
}

function CommandSeparator({
	className,
	...props
}: React.ComponentProps<typeof CommandPrimitive.Separator>) {
	return (
		<CommandPrimitive.Separator
			data-slot="command-separator"
			className={cn("-mx-1 h-px bg-border", className)}
			{...props}
		/>
	);
}

// `onSelect` is kept as the kit's prop name and mapped onto Base UI's item
// `onClick`, which fires for a pointer press and for Enter on the highlighted
// item alike — the same two gestures the previous `onSelect` covered.
function CommandItem({
	className,
	children,
	onSelect,
	value,
	...props
}: Omit<
	React.ComponentProps<typeof CommandPrimitive.Item>,
	"onClick" | "children"
> & {
	onSelect?: (value: string) => void;
	children: React.ReactNode;
}) {
	return (
		<CommandPrimitive.Item
			data-slot="command-item"
			value={value}
			onClick={() => onSelect?.(typeof value === "string" ? value : "")}
			className={cn(
				"group/command-item relative flex cursor-default items-center gap-2 rounded-sm px-2 py-1.5 type-command-item outline-hidden select-none in-data-[slot=dialog-content]:rounded-lg! data-disabled:pointer-events-none data-disabled:opacity-50 data-highlighted:bg-muted data-highlighted:text-foreground [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 data-highlighted:*:[svg]:text-foreground",
				className,
			)}
			{...props}
		>
			{children}
		</CommandPrimitive.Item>
	);
}

function CommandShortcut({
	className,
	...props
}: React.ComponentProps<"span">) {
	return (
		<span
			data-slot="command-shortcut"
			className={cn(
				"ml-auto type-command-shortcut text-muted-foreground group-data-highlighted/command-item:text-foreground",
				className,
			)}
			{...props}
		/>
	);
}

const useCommandFilter = CommandPrimitive.useFilter;

export {
	Command,
	CommandDialog,
	CommandEmpty,
	CommandGroup,
	CommandInput,
	CommandItem,
	CommandList,
	CommandSeparator,
	CommandShortcut,
	useCommandFilter,
};
