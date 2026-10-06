"use client";

import { ChevronRightIcon } from "lucide-react";
import {
	type CSSProperties,
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useId,
	useRef,
	useState,
} from "react";
import { cn } from "./cn.js";
import { DelayedLoading } from "./delayed-loading.js";

export interface TreeNode {
	id: string;
	label: ReactNode;
	/** Plain text for typeahead and accessible naming when label is rich content. */
	textValue: string;
	children?: readonly TreeNode[];
	/** Children exist but have not yet been provided. Expanding requests them. */
	hasChildren?: boolean;
	loading?: boolean;
	disabled?: boolean;
}
export interface TreeProps {
	items: readonly TreeNode[];
	label: string;
	selectedId?: string;
	/** Reveal a changed target without stealing DOM focus; distinct from selection. */
	focusedId?: string;
	onSelect?: (node: TreeNode) => void;
	expandedIds?: readonly string[];
	defaultExpandedIds?: readonly string[];
	onExpandedChange?: (ids: string[]) => void;
	onLoadChildren?: (node: TreeNode) => void;
	/** Window large trees with an explicit fixed row height. Omit to render all rows. */
	virtualize?: { height: number; rowHeight: number; overscan?: number };
	className?: string;
	style?: CSSProperties;
}
interface Row {
	node: TreeNode;
	level: number;
	position: number;
	size: number;
	parent?: string;
}
function flatten(
	items: readonly TreeNode[],
	expanded: Set<string>,
	parent?: string,
	level = 1,
): Row[] {
	return items.flatMap((node, index) => [
		{ node, level, position: index + 1, size: items.length, parent },
		...(expanded.has(node.id)
			? flatten(node.children ?? [], expanded, node.id, level + 1)
			: []),
	]);
}
function ancestorIds(
	items: readonly TreeNode[],
	id: string | undefined,
): string[] {
	for (const node of items) {
		if (node.id === id) return [node.id];
		const path = ancestorIds(node.children ?? [], id);
		if (path.length) return [node.id, ...path];
	}
	return [];
}
const expandable = (node: TreeNode) =>
	Boolean(node.hasChildren || node.children?.length);

/** A data-in, single-selection tree. Fetching and node meanings belong to its caller. */
export function Tree({
	items,
	label,
	selectedId,
	focusedId,
	onSelect,
	expandedIds,
	defaultExpandedIds = [],
	onExpandedChange,
	onLoadChildren,
	virtualize,
	className,
	style,
}: TreeProps) {
	const prefix = useId();
	const root = useRef<HTMLDivElement>(null);
	const [internalExpanded, setInternalExpanded] =
		useState<readonly string[]>(defaultExpandedIds);
	const expanded = new Set(expandedIds ?? internalExpanded);
	const rows = flatten(items, expanded);
	const [activeId, setActiveId] = useState<string | undefined>(selectedId);
	const ancestor = ancestorIds(items, activeId)
		.reverse()
		.find((id) => rows.some((row) => row.node.id === id));
	const active =
		rows.find((row) => row.node.id === (activeId ?? selectedId)) ??
		rows.find((row) => row.node.id === ancestor) ??
		rows.find((row) => row.node.id === selectedId) ??
		rows[0];
	const [scrollTop, setScrollTop] = useState(0);
	const search = useRef({ text: "", at: 0 });
	const windowed =
		virtualize &&
		Number.isFinite(virtualize.height) &&
		virtualize.height > 0 &&
		Number.isFinite(virtualize.rowHeight) &&
		virtualize.rowHeight > 0
			? virtualize
			: undefined;
	const viewportHeight = windowed?.height;
	const rowHeight = windowed?.rowHeight;
	const rowId = (id: string) => `${prefix}-${encodeURIComponent(id)}`;
	const activeIndex = active ? rows.indexOf(active) : -1;
	const overscan = Math.max(0, Math.floor(windowed?.overscan ?? 3));
	const start = windowed
		? Math.max(0, Math.floor(scrollTop / windowed.rowHeight) - overscan)
		: 0;
	const end = windowed
		? Math.min(
				rows.length,
				Math.ceil((scrollTop + windowed.height) / windowed.rowHeight) +
					overscan,
			)
		: rows.length;
	// Keep the active descendant mounted even when the pointer scrolls it out of view.
	const indices = new Set(
		Array.from(
			{ length: Math.max(0, end - start) },
			(_, index) => start + index,
		),
	);
	if (activeIndex >= 0) indices.add(activeIndex);

	useEffect(() => {
		// A shorter refreshed tree must not leave an empty viewport below its last row.
		if (!viewportHeight || !rowHeight || !root.current) return;
		const limit = Math.max(0, rows.length * rowHeight - viewportHeight);
		if (root.current.scrollTop > limit) {
			root.current.scrollTop = limit;
			setScrollTop(limit);
		}
	}, [rows.length, viewportHeight, rowHeight]);

	const revealIndex = rows.findIndex((row) => row.node.id === focusedId);
	useEffect(() => {
		if (revealIndex < 0 || !focusedId) return;
		setActiveId(focusedId);
		if (viewportHeight && rowHeight && root.current) {
			const top = revealIndex * rowHeight;
			const current = root.current.scrollTop;
			const next =
				top < current
					? top
					: top + rowHeight > current + viewportHeight
						? top + rowHeight - viewportHeight
						: current;
			root.current.scrollTop = next;
			setScrollTop(next);
		}
	}, [focusedId, revealIndex, viewportHeight, rowHeight]);

	function focus(row: Row | undefined) {
		if (!row) return;
		setActiveId(row.node.id);
		root.current?.focus();
		if (windowed && root.current) {
			const top = rows.indexOf(row) * windowed.rowHeight;
			const current = root.current.scrollTop;
			const next =
				top < current
					? top
					: top + windowed.rowHeight > current + windowed.height
						? top + windowed.rowHeight - windowed.height
						: current;
			root.current.scrollTop = next;
			setScrollTop(next);
		}
	}
	function toggle(row: Row, open = !expanded.has(row.node.id)) {
		if (!expandable(row.node) || row.node.disabled) return;
		const next = new Set(expanded);
		if (open) next.add(row.node.id);
		else next.delete(row.node.id);
		if (expandedIds === undefined) setInternalExpanded([...next]);
		onExpandedChange?.([...next]);
		if (
			open &&
			!expanded.has(row.node.id) &&
			row.node.hasChildren &&
			!row.node.children?.length &&
			!row.node.loading
		)
			onLoadChildren?.(row.node);
	}
	function keyDown(event: KeyboardEvent<HTMLDivElement>) {
		if (
			event.target !== event.currentTarget ||
			!active ||
			event.altKey ||
			event.ctrlKey ||
			event.metaKey
		)
			return;
		const index = rows.indexOf(active);
		switch (event.key) {
			case "ArrowDown":
				event.preventDefault();
				focus(rows[Math.min(rows.length - 1, index + 1)]);
				break;
			case "ArrowUp":
				event.preventDefault();
				focus(rows[Math.max(0, index - 1)]);
				break;
			case "Home":
				event.preventDefault();
				focus(rows[0]);
				break;
			case "End":
				event.preventDefault();
				focus(rows.at(-1));
				break;
			case "ArrowRight":
				event.preventDefault();
				if (!expanded.has(active.node.id)) toggle(active, true);
				else if (rows[index + 1]?.parent === active.node.id)
					focus(rows[index + 1]);
				break;
			case "ArrowLeft":
				event.preventDefault();
				if (expanded.has(active.node.id)) toggle(active, false);
				else focus(rows.find((row) => row.node.id === active.parent));
				break;
			case "Enter":
			case " ":
				event.preventDefault();
				if (!active.node.disabled) onSelect?.(active.node);
				break;
			default: {
				if (event.key.length !== 1 || event.nativeEvent.isComposing) break;
				search.current.text =
					Date.now() - search.current.at > 700
						? event.key
						: search.current.text + event.key;
				search.current.at = Date.now();
				const text = search.current.text.toLocaleLowerCase();
				const candidates = [
					...rows.slice(index + 1),
					...rows.slice(0, index + 1),
				];
				focus(
					candidates.find((row) =>
						row.node.textValue.toLocaleLowerCase().startsWith(text),
					),
				);
			}
		}
	}
	return (
		<div
			ref={root}
			role="tree"
			tabIndex={0}
			aria-label={label}
			aria-activedescendant={active ? rowId(active.node.id) : undefined}
			data-slot="tree"
			className={cn(
				"rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-ring type-list-item",
				className,
			)}
			style={{
				...style,
				...(windowed ? { height: windowed.height, overflow: "auto" } : {}),
			}}
			onKeyDown={keyDown}
			onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
		>
			<div
				role="presentation"
				style={
					windowed
						? { height: rows.length * windowed.rowHeight, position: "relative" }
						: undefined
				}
			>
				{[...indices]
					.sort((a, b) => a - b)
					.map((index) => {
						const row = rows[index];
						if (!row) return null;
						const { node } = row;
						return (
							// biome-ignore lint/a11y/useFocusableInteractive: The tree root owns focus through aria-activedescendant.
							// biome-ignore lint/a11y/useKeyWithClickEvents: Root handles keyboard selection for the active descendant.
							<div
								key={node.id}
								id={rowId(node.id)}
								role="treeitem"
								aria-label={node.textValue}
								aria-level={row.level}
								aria-posinset={row.position}
								aria-setsize={row.size}
								aria-expanded={
									expandable(node) ? expanded.has(node.id) : undefined
								}
								aria-selected={selectedId === node.id}
								aria-disabled={node.disabled || undefined}
								aria-busy={node.loading || undefined}
								data-node-id={node.id}
								data-active={active?.node.id === node.id || undefined}
								className={cn(
									"rounded-sm border border-transparent px-2 py-1",
									selectedId === node.id && "bg-accent text-accent-foreground",
									active?.node.id === node.id && "border-ring",
									node.disabled && "opacity-50",
								)}
								style={{
									display: "flex",
									alignItems: "center",
									gap: "0.5rem",
									paddingInlineStart: `${(row.level - 1) * 1.25 + 0.5}rem`,
									...(windowed
										? {
												position: "absolute",
												top: index * windowed.rowHeight,
												height: windowed.rowHeight,
												boxSizing: "border-box",
												width: "100%",
												whiteSpace: "nowrap",
												overflow: "hidden",
											}
										: {}),
								}}
								onClick={() => {
									focus(row);
									if (!node.disabled) onSelect?.(node);
								}}
							>
								{expandable(node) ? (
									<button
										type="button"
										tabIndex={-1}
										disabled={node.disabled}
										aria-label={`${expanded.has(node.id) ? "Collapse" : "Expand"} ${node.textValue}`}
										onClick={(event) => {
											event.stopPropagation();
											focus(row);
											toggle(row);
										}}
										style={{ display: "inline-flex", flexShrink: 0 }}
									>
										<ChevronRightIcon
											aria-hidden
											className="size-4"
											style={{
												transform: expanded.has(node.id)
													? "rotate(90deg)"
													: undefined,
											}}
										/>
									</button>
								) : (
									<span aria-hidden style={{ width: "1rem", flexShrink: 0 }} />
								)}
								{node.label}
								<DelayedLoading
									active={Boolean(node.loading)}
									label={`Loading ${node.textValue}`}
								/>
							</div>
						);
					})}
			</div>
		</div>
	);
}
