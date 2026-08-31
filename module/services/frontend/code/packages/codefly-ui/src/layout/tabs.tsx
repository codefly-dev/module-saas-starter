"use client";

// A pure, accessible tabs primitive for solution pages. It takes data-in tabs and
// paints an ARIA tablist — no host context, no SDK, no data fetching — so the host
// app and a solution's Module-Federation remote compose pages from one shared
// package instance. Works controlled (pass `active` + `onChange`) or uncontrolled
// (pass `initial`, or default to the first tab).

import {
	type KeyboardEvent,
	type ReactNode,
	useCallback,
	useId,
	useRef,
	useState,
} from "react";
import { cn } from "./cn.js";

export interface TabItem {
	id: string;
	label: ReactNode;
	content: ReactNode;
}

export interface TabsProps {
	tabs: TabItem[];
	/** Uncontrolled initial selection (defaults to the first tab). */
	initial?: string;
	/** Controlled selection; pair with `onChange`. */
	active?: string;
	onChange?: (id: string) => void;
	className?: string;
}

/**
 * Render a tablist with keyboard navigation (Left/Right/Home/End) and the ARIA
 * wiring a screen reader needs. Selection is controlled when `active` is passed,
 * otherwise tracked internally starting from `initial`.
 */
export function Tabs({
	tabs,
	initial,
	active,
	onChange,
	className,
}: TabsProps) {
	const baseId = useId();
	const [internal, setInternal] = useState(() => initial ?? tabs[0]?.id);
	const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});

	// Reconcile the requested selection against the current tabs: an `initial` or
	// `active` id that matches no tab (a typo, or an id whose tab has since gone)
	// would otherwise select nothing — leaving every tab out of the tab order and
	// rendering no panel, a silently dead and keyboard-unreachable widget. Fall
	// back to the first tab so the component is always usable.
	const requested = active ?? internal;
	const selected = tabs.some((tab) => tab.id === requested)
		? requested
		: tabs[0]?.id;

	const select = useCallback(
		(id: string) => {
			if (active === undefined) setInternal(id);
			onChange?.(id);
		},
		[active, onChange],
	);

	const onKeyDown = useCallback(
		(event: KeyboardEvent<HTMLButtonElement>) => {
			const index = tabs.findIndex((tab) => tab.id === selected);
			let next: number | undefined;
			switch (event.key) {
				case "ArrowRight":
					next = (index + 1) % tabs.length;
					break;
				case "ArrowLeft":
					next = (index - 1 + tabs.length) % tabs.length;
					break;
				case "Home":
					next = 0;
					break;
				case "End":
					next = tabs.length - 1;
					break;
				default:
					return;
			}
			event.preventDefault();
			const nextTab = tabs[next];
			select(nextTab.id);
			tabRefs.current[nextTab.id]?.focus();
		},
		[tabs, selected, select],
	);

	const activeTab = tabs.find((tab) => tab.id === selected);

	return (
		<div className={cn("space-y-4", className)}>
			<div role="tablist" className="flex gap-1 border-b">
				{tabs.map((tab) => {
					const isSelected = tab.id === selected;
					return (
						<button
							key={tab.id}
							type="button"
							role="tab"
							id={`${baseId}-tab-${tab.id}`}
							aria-selected={isSelected}
							aria-controls={`${baseId}-panel-${tab.id}`}
							tabIndex={isSelected ? 0 : -1}
							ref={(node) => {
								tabRefs.current[tab.id] = node;
							}}
							onClick={() => select(tab.id)}
							onKeyDown={onKeyDown}
							className={cn(
								"-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
								isSelected
									? "border-primary text-foreground"
									: "border-transparent text-muted-foreground hover:text-foreground",
							)}
						>
							{tab.label}
						</button>
					);
				})}
			</div>
			{activeTab && (
				<div
					role="tabpanel"
					id={`${baseId}-panel-${activeTab.id}`}
					aria-labelledby={`${baseId}-tab-${activeTab.id}`}
					tabIndex={0}
				>
					{activeTab.content}
				</div>
			)}
		</div>
	);
}
