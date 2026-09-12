"use client";

import { type ReactNode, useState } from "react";
import { TabsRoot, TabsList, TabsTrigger, TabsContent } from "./tabs-root.js";

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
	/**
	 * Render every panel and hide the inactive ones instead of unmounting them,
	 * so a panel's React state (a live transcript, an open citation) survives a
	 * tab switch. Hiding is via the `hidden` attribute (`display: none`), so the
	 * panel keeps its component state but not layout-derived DOM state — a
	 * scroll position inside a hidden panel is not preserved. Hidden panels stay
	 * out of the accessibility tree and the tab order. Defaults to unmounting.
	 */
	keepMounted?: boolean;
	className?: string;
}

export function Tabs({
	tabs,
	initial,
	active,
	onChange,
	keepMounted = false,
	className,
}: TabsProps) {
	const [internal, setInternal] = useState(() => initial ?? tabs[0]?.id);
	const requested = active ?? internal;
	const selected = tabs.some((tab) => tab.id === requested)
		? requested
		: tabs[0]?.id;
	return (
		<TabsRoot
			value={selected ?? null}
			onValueChange={(value) => {
				const id = value as string;
				if (active === undefined) setInternal(id);
				onChange?.(id);
			}}
			className={className}
		>
			<TabsList variant="line" activateOnFocus>
				{tabs.map((tab) => (
					<TabsTrigger key={tab.id} value={tab.id}>
						{tab.label}
					</TabsTrigger>
				))}
			</TabsList>
			{tabs
				.filter((tab) => keepMounted || tab.id === selected)
				.map((tab) => (
					<TabsContent
						key={tab.id}
						value={tab.id}
						keepMounted={keepMounted}
						hidden={tab.id !== selected}
					>
						{tab.content}
					</TabsContent>
				))}
		</TabsRoot>
	);
}
