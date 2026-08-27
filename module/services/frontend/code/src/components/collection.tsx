"use client";

/**
 * Collection — a data-driven renderer for any hierarchical Document
 * collection. A consumer hands it Documents (`CollectionData`) and a
 * view descriptor (`CollectionView`); the renderer owns the tree /
 * table / gallery chrome and — first-class — the loading / error /
 * empty states, so a page never hand-rolls them.
 *
 * It is deliberately content-agnostic. A Document is `{ id, label,
 * facets, children }`; the renderer decides *nothing* about what a
 * facet means. Presentation — icon, badge, color, tooltip, which
 * facets become columns, which facet groups a board — is dictated
 * entirely by the descriptor. The same `documents` render as a tree,
 * a table, or a gallery by swapping only `view.type`.
 */

import { ChevronRight } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

export type FacetValue = string | number | boolean | null | undefined;

/**
 * A node in the collection. `children` holds resolved descendants;
 * `hasChildren` marks a node whose subtree has not been fetched yet —
 * expanding it triggers a lazy load (see `TreeView.onLoadChildren`).
 */
export interface CollectionDocument {
	id: string;
	label: string;
	facets?: Record<string, FacetValue>;
	children?: CollectionDocument[];
	hasChildren?: boolean;
}

export interface CollectionData {
	documents: CollectionDocument[];
	isLoading?: boolean;
	error?: unknown;
	emptyMessage?: string;
}

/**
 * Presentational overlay for a Document, computed from its facets by a
 * descriptor-supplied provider — the VS Code `FileDecorationProvider`
 * shape. Every field is optional; the renderer skips what is absent.
 */
export interface Decoration {
	icon?: ReactNode;
	badge?: string;
	/** Any CSS color for the label — a token (`var(--…)`) or literal. */
	color?: string;
	tooltip?: string;
}

export type DecorationProvider = (
	doc: CollectionDocument,
) => Decoration | undefined;

interface ViewBase {
	decorate?: DecorationProvider;
}

export interface TreeView extends ViewBase {
	type: "tree";
	/** Called when a node with an unfetched subtree (`hasChildren`) expands. */
	onLoadChildren?: (doc: CollectionDocument) => void;
	/** Ids whose subtree is currently being fetched — rendered as a spinner row. */
	loadingIds?: string[];
	/** Window the row list once visible rows exceed this count. */
	virtualizeThreshold?: number;
	/** Fixed row height (px) the window math assumes. */
	rowHeight?: number;
	/** Scroll-viewport height (px) when windowing. */
	height?: number;
}

export interface TableColumn {
	/** Facet key to read, or the sentinel `"label"` for the Document label. */
	facet: string;
	header: string;
	format?: (value: FacetValue, doc: CollectionDocument) => ReactNode;
}

export interface TableView extends ViewBase {
	type: "table";
	columns: TableColumn[];
}

export interface GalleryView extends ViewBase {
	type: "gallery";
	/** Facet key whose values become the boards. */
	groupBy: string;
	/** Board heading for Documents missing the `groupBy` facet. */
	ungroupedLabel?: string;
}

export type CollectionView = TreeView | TableView | GalleryView;

/**
 * Retained content wins over transient states: while the collection
 * already holds Documents, a background refetch or its error never
 * blanks the view. Only an empty collection surfaces error → loading →
 * empty, in that order.
 */
function TopLevelState({ data }: { data: CollectionData }) {
	if (data.error) {
		return <p className="text-sm text-destructive">Failed to load.</p>;
	}
	if (data.isLoading) {
		return (
			<div className="space-y-2">
				<Skeleton className="h-6 w-full" />
				<Skeleton className="h-6 w-5/6" />
				<Skeleton className="h-6 w-4/6" />
			</div>
		);
	}
	return (
		<p className="text-sm text-muted-foreground">
			{data.emptyMessage ?? "No documents."}
		</p>
	);
}

function DecoratedLabel({
	doc,
	decorate,
	className,
}: {
	doc: CollectionDocument;
	decorate?: DecorationProvider;
	className?: string;
}) {
	const decoration = decorate?.(doc);
	return (
		<span
			className={cn("inline-flex items-center gap-1.5", className)}
			title={decoration?.tooltip}
		>
			{decoration?.icon && (
				<span className="shrink-0 text-muted-foreground">
					{decoration.icon}
				</span>
			)}
			<span style={decoration?.color ? { color: decoration.color } : undefined}>
				{doc.label}
			</span>
			{decoration?.badge && (
				<Badge variant="secondary" className="ml-1">
					{decoration.badge}
				</Badge>
			)}
		</span>
	);
}

type FlatRow =
	| { kind: "doc"; doc: CollectionDocument; depth: number }
	| { kind: "loading"; id: string; depth: number };

/** Depth-first flatten of the expanded subtree into renderable rows. */
function flattenTree(
	docs: CollectionDocument[],
	expanded: Set<string>,
	loadingIds: Set<string>,
	depth: number,
	out: FlatRow[],
): FlatRow[] {
	for (const doc of docs) {
		out.push({ kind: "doc", doc, depth });
		if (!expanded.has(doc.id)) {
			continue;
		}
		if (doc.children?.length) {
			flattenTree(doc.children, expanded, loadingIds, depth + 1, out);
		} else if (loadingIds.has(doc.id)) {
			out.push({ kind: "loading", id: doc.id, depth: depth + 1 });
		}
	}
	return out;
}

function isExpandable(doc: CollectionDocument): boolean {
	return Boolean(doc.children?.length || doc.hasChildren);
}

function TreeRow({
	row,
	expanded,
	onToggle,
	decorate,
}: {
	row: Extract<FlatRow, { kind: "doc" }>;
	expanded: boolean;
	onToggle: (doc: CollectionDocument) => void;
	decorate?: DecorationProvider;
}) {
	const { doc, depth } = row;
	const expandable = isExpandable(doc);
	return (
		<div
			className="flex items-center gap-1 rounded-sm px-1 py-1 hover:bg-muted/50"
			style={{ paddingLeft: `${depth * 16 + 4}px` }}
		>
			{expandable ? (
				<button
					type="button"
					aria-label={expanded ? "Collapse" : "Expand"}
					aria-expanded={expanded}
					onClick={() => onToggle(doc)}
					className="flex h-4 w-4 shrink-0 items-center justify-center text-muted-foreground"
				>
					<ChevronRight
						className={cn(
							"h-3.5 w-3.5 transition-transform",
							expanded && "rotate-90",
						)}
					/>
				</button>
			) : (
				<span className="h-4 w-4 shrink-0" />
			)}
			<DecoratedLabel doc={doc} decorate={decorate} className="text-sm" />
		</div>
	);
}

function TreeViewRenderer({
	documents,
	view,
}: {
	documents: CollectionDocument[];
	view: TreeView;
}) {
	const [expanded, setExpanded] = useState<Set<string>>(new Set());
	const [scrollTop, setScrollTop] = useState(0);
	const loadingIds = useMemo(
		() => new Set(view.loadingIds ?? []),
		[view.loadingIds],
	);

	const toggle = (doc: CollectionDocument) => {
		setExpanded((prev) => {
			const next = new Set(prev);
			if (next.has(doc.id)) {
				next.delete(doc.id);
			} else {
				next.add(doc.id);
				// Expanding an unfetched subtree asks the consumer to load it.
				if (doc.hasChildren && !doc.children?.length) {
					view.onLoadChildren?.(doc);
				}
			}
			return next;
		});
	};

	const rows = useMemo(
		() => flattenTree(documents, expanded, loadingIds, 0, []),
		[documents, expanded, loadingIds],
	);

	const threshold = view.virtualizeThreshold ?? 200;
	const rowHeight = view.rowHeight ?? 32;
	const height = view.height ?? 480;

	const renderRow = (row: FlatRow) => {
		if (row.kind === "loading") {
			return (
				<div
					key={`loading-${row.id}`}
					className="flex items-center px-1 py-1 text-xs text-muted-foreground"
					style={{ paddingLeft: `${row.depth * 16 + 24}px` }}
				>
					Loading…
				</div>
			);
		}
		return (
			<TreeRow
				key={row.doc.id}
				row={row}
				expanded={expanded.has(row.doc.id)}
				onToggle={toggle}
				decorate={view.decorate}
			/>
		);
	};

	if (rows.length <= threshold) {
		return <div role="tree">{rows.map(renderRow)}</div>;
	}

	// Fixed-height windowing: render only the rows intersecting the
	// viewport (plus a small overscan) and pad with spacers so the
	// scrollbar still reflects the full row count.
	const overscan = 5;
	const visibleCount = Math.ceil(height / rowHeight);
	const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
	const end = Math.min(rows.length, start + visibleCount + overscan * 2);
	const window = rows.slice(start, end);

	return (
		<div
			role="tree"
			className="overflow-auto"
			style={{ height }}
			onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
		>
			<div style={{ height: rows.length * rowHeight, position: "relative" }}>
				<div style={{ transform: `translateY(${start * rowHeight}px)` }}>
					{window.map(renderRow)}
				</div>
			</div>
		</div>
	);
}

/** Fully flatten every Document (ignoring expand state) for flat views. */
function flattenAll(
	docs: CollectionDocument[],
	depth: number,
	out: { doc: CollectionDocument; depth: number }[],
): { doc: CollectionDocument; depth: number }[] {
	for (const doc of docs) {
		out.push({ doc, depth });
		if (doc.children?.length) {
			flattenAll(doc.children, depth + 1, out);
		}
	}
	return out;
}

function TableViewRenderer({
	documents,
	view,
}: {
	documents: CollectionDocument[];
	view: TableView;
}) {
	const rows = useMemo(() => flattenAll(documents, 0, []), [documents]);
	return (
		<Table>
			<TableHeader>
				<TableRow>
					{view.columns.map((col) => (
						<TableHead key={col.facet}>{col.header}</TableHead>
					))}
				</TableRow>
			</TableHeader>
			<TableBody>
				{rows.map(({ doc, depth }) => (
					<TableRow key={doc.id}>
						{view.columns.map((col) => {
							if (col.facet === "label") {
								return (
									<TableCell key={col.facet}>
										<span style={{ paddingLeft: `${depth * 16}px` }}>
											<DecoratedLabel doc={doc} decorate={view.decorate} />
										</span>
									</TableCell>
								);
							}
							const value = doc.facets?.[col.facet];
							return (
								<TableCell key={col.facet}>
									{col.format
										? col.format(value, doc)
										: value == null
											? ""
											: String(value)}
								</TableCell>
							);
						})}
					</TableRow>
				))}
			</TableBody>
		</Table>
	);
}

function GalleryViewRenderer({
	documents,
	view,
}: {
	documents: CollectionDocument[];
	view: GalleryView;
}) {
	const boards = useMemo(() => {
		const groups = new Map<string, CollectionDocument[]>();
		const order: string[] = [];
		for (const { doc } of flattenAll(documents, 0, [])) {
			const raw = doc.facets?.[view.groupBy];
			const key =
				raw == null || raw === ""
					? (view.ungroupedLabel ?? "Ungrouped")
					: String(raw);
			if (!groups.has(key)) {
				groups.set(key, []);
				order.push(key);
			}
			groups.get(key)?.push(doc);
		}
		return order.map((key) => ({ key, docs: groups.get(key) ?? [] }));
	}, [documents, view.groupBy, view.ungroupedLabel]);

	return (
		<div className="flex gap-4 overflow-x-auto">
			{boards.map((board) => (
				<div key={board.key} className="w-64 shrink-0 space-y-2">
					<div className="flex items-center justify-between px-1">
						<h3 className="text-sm font-medium">{board.key}</h3>
						<Badge variant="outline">{board.docs.length}</Badge>
					</div>
					<div className="space-y-2">
						{board.docs.map((doc) => (
							<div
								key={doc.id}
								className="rounded-md border bg-card p-3 text-sm shadow-sm"
							>
								<DecoratedLabel doc={doc} decorate={view.decorate} />
							</div>
						))}
					</div>
				</div>
			))}
		</div>
	);
}

export function Collection({
	data,
	view,
}: {
	data: CollectionData;
	view: CollectionView;
}) {
	if (data.documents.length === 0) {
		return <TopLevelState data={data} />;
	}
	switch (view.type) {
		case "tree":
			return <TreeViewRenderer documents={data.documents} view={view} />;
		case "table":
			return <TableViewRenderer documents={data.documents} view={view} />;
		case "gallery":
			return <GalleryViewRenderer documents={data.documents} view={view} />;
		default: {
			// Compile-time exhaustiveness: a new view type must be handled
			// here or this assignment fails to type-check.
			const _exhaustive: never = view;
			return _exhaustive;
		}
	}
}
