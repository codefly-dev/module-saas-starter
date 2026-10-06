"use client";

import { Collapsible } from "@base-ui/react/collapsible";
import { ChevronRightIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "./cn.js";

export interface DisclosureProps extends Omit<Collapsible.Root.Props, "title"> {
	title: ReactNode;
	/** Retain descendant state while closed, for drafts or nested controls. */
	keepMounted?: boolean;
	/** Heading semantics belong to the surrounding document. */
	headingLevel?: 2 | 3 | 4 | 5 | 6;
}

/** An expandable section; the consumer owns its label, content and state. */
export function Disclosure({
	title,
	keepMounted = false,
	headingLevel = 3,
	children,
	className,
	...props
}: DisclosureProps) {
	const Heading = `h${headingLevel}` as "h3";
	return (
		<Collapsible.Root
			data-slot="disclosure"
			className={cn("rounded-lg border border-border", className)}
			{...props}
		>
			<Heading style={{ margin: 0 }}>
				<Collapsible.Trigger className="group flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left type-section-title outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50">
					<ChevronRightIcon
						aria-hidden
						className="size-4 shrink-0 transition-transform group-data-[panel-open]:rotate-90"
					/>
					{title}
				</Collapsible.Trigger>
			</Heading>
			<Collapsible.Panel
				keepMounted={keepMounted}
				data-slot="disclosure-content"
				className="px-3 pb-3 type-card-description"
			>
				{children}
			</Collapsible.Panel>
		</Collapsible.Root>
	);
}
