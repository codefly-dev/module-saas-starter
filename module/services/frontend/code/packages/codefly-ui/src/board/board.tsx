"use client";

/**
 * Board — a collection in columns, one per value of a field, whose cards a
 * reader drags from one column to another.
 *
 * **The board never commits a move.** A drop, or a choice in a card's Move
 * menu, calls `onMove(item, column)` and nothing else: the consumer confirms it
 * with the reader, writes it, refuses it, and passes `items` back with the new
 * column. A board that moved the card itself would show a state the server never
 * agreed to, and would have to undo it when the write failed.
 *
 * **Dragging is never the only way.** A pointer drag is a gesture a keyboard
 * cannot make and a screen reader cannot see, so every card carries two real
 * controls in reading order: one that opens it, and a **Move** menu listing the
 * columns it may go to. That is the same answer `SortableGrid` documents — the
 * grid has no keyboard sensor and asks its caller for the alternative — except
 * that a board knows what the alternative is and ships it.
 *
 * Nothing here knows what a column means: no status, no workflow, no vocabulary.
 * `columnOf` names the field, `columns` names its values, and a consumer that
 * wants four states or two gets them the same way.
 */
import {
	type Announcements,
	DndContext,
	DragOverlay,
	MouseSensor,
	pointerWithin,
	type ScreenReaderInstructions,
	TouchSensor,
	useDraggable,
	useDroppable,
	useSensor,
	useSensors,
} from "@dnd-kit/core";
import { type ReactNode, useId, useMemo, useState } from "react";
import { Badge } from "../layout/badge.js";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
} from "../layout/dropdown-menu.js";
import { Input } from "../layout/input.js";
import { cn } from "./cn.js";

/** One column: a value of the field the board groups by, and what to call it. */
export interface BoardColumn {
	/** The value `columnOf` returns for the items that belong here. */
	id: string;
	/** The column's heading, and its name in the Move menu. */
	label: string;
	/** What the column says when nothing is in it. Default *Nothing here*. */
	empty?: ReactNode;
}

export interface BoardProps<Item> {
	/** Everything on the board; the column each lands in is `columnOf`'s answer. */
	items: readonly Item[];
	/** The columns, in the order they are shown. An item whose column is not here is not shown. */
	columns: readonly BoardColumn[];
	/** The field the board groups by, read off one item. */
	columnOf: (item: Item) => string;
	/** A stable id for an item, unique across the board. */
	idOf: (item: Item) => string;
	/**
	 * What to call one item: the accessible name of its open control and its Move
	 * menu, and how a drag is announced. Not the card's contents — the name a
	 * reader would use for it in a sentence.
	 */
	labelOf: (item: Item) => string;
	/**
	 * The card's contents. With `onOpen` they sit inside the control that opens
	 * the card, so keep them non-interactive: a link or a button nested in a
	 * button is two targets announced as one.
	 */
	renderCard: (item: Item) => ReactNode;
	/** Opening an item. Without it the card is text rather than a control. */
	onOpen?: (item: Item) => void;
	/**
	 * An item was dropped on, or chosen to move to, another column. The board has
	 * changed nothing: confirm with the reader, write it, and pass `items` back.
	 * Without it the board is read-only and no card drags.
	 */
	onMove?: (item: Item, column: BoardColumn) => void;
	/**
	 * Which moves are offered at all — a column a card may not enter accepts no
	 * drop and is absent from its Move menu. Default: every column but its own.
	 */
	canMove?: (item: Item, column: BoardColumn) => boolean;
	/**
	 * The text the search box matches an item against. Without it there is no
	 * search box: only the consumer knows which of an item's fields are worth
	 * searching.
	 */
	searchText?: (item: Item) => string;
	/** The search box's accessible name and placeholder. */
	searchLabel?: string;
	/** How items are ordered inside a column. Default: the order of `items`. */
	compare?: (a: Item, b: Item) => number;
	/**
	 * How narrow a column may get before the board wraps to another row. Default
	 * `16rem`. A CSS length: it is read as one, inline.
	 */
	columnWidth?: string;
	className?: string;
}

// A card is its own droppable-free draggable; the column it lands in is the
// droppable. So the pointer has to be over the column, not over a card in it,
// which is what makes dropping onto an empty column work.
const CARD = "card:";
const COLUMN = "column:";

function Card<Item>({
	id,
	item,
	draggable,
	children,
}: {
	id: string;
	item: Item;
	draggable: boolean;
	children: ReactNode;
}) {
	const { setNodeRef, listeners, isDragging } = useDraggable({
		id: `${CARD}${id}`,
		disabled: !draggable,
		data: { item },
	});
	// dnd-kit's `attributes` are deliberately not spread: they would make the
	// list item itself a focusable `role="button"`, announced beside the two real
	// controls the card already carries, and a keyboard would land on a target
	// that does nothing without a sensor behind it.
	return (
		<li
			ref={setNodeRef}
			{...listeners}
			data-board-card={id}
			data-dragging={isDragging || undefined}
			className={cn(
				"rounded-md border border-border bg-card hover:border-ring",
				draggable && "touch-manipulation",
			)}
			style={{
				cursor: draggable ? "grab" : undefined,
				opacity: isDragging ? 0.4 : undefined,
			}}
		>
			{children}
		</li>
	);
}

function Column({
	column,
	count,
	accepts,
	children,
}: {
	column: BoardColumn;
	count: number;
	accepts: boolean;
	children: ReactNode;
}) {
	const { setNodeRef, isOver } = useDroppable({
		id: `${COLUMN}${column.id}`,
		disabled: !accepts,
		data: { column },
	});
	return (
		<section
			ref={setNodeRef}
			aria-label={column.label}
			data-board-column={column.id}
			className={cn(
				"rounded-lg border border-border bg-muted p-3",
				isOver && accepts && "border-ring",
			)}
			style={{ display: "flex", flexDirection: "column", gap: "0.75rem" }}
		>
			<header
				style={{
					display: "flex",
					alignItems: "center",
					justifyContent: "space-between",
					gap: "0.5rem",
				}}
			>
				<h3 className="type-card-title-sm">{column.label}</h3>
				<Badge variant="secondary">{count}</Badge>
			</header>
			{children}
		</section>
	);
}

export function Board<Item>({
	items,
	columns,
	columnOf,
	idOf,
	labelOf,
	renderCard,
	onOpen,
	onMove,
	canMove,
	searchText,
	searchLabel = "Search",
	compare,
	columnWidth = "16rem",
	className,
}: BoardProps<Item>) {
	// A stable id keeps the ids dnd-kit writes into the page the same on the
	// server and the client, as in SortableGrid.
	const contextId = useId();
	const [query, setQuery] = useState("");
	const [dragged, setDragged] = useState<string | null>(null);

	const shown = useMemo(() => {
		const wanted = query.trim().toLowerCase();
		const matching =
			wanted === "" || !searchText
				? [...items]
				: items.filter((item) =>
						searchText(item).toLowerCase().includes(wanted),
					);
		return compare ? matching.sort(compare) : matching;
	}, [items, query, searchText, compare]);

	const byColumn = useMemo(() => {
		const grouped = new Map<string, Item[]>(
			columns.map((column) => [column.id, []]),
		);
		for (const item of shown) grouped.get(columnOf(item))?.push(item);
		return grouped;
	}, [shown, columns, columnOf]);

	const movable = (item: Item, column: BoardColumn) =>
		!!onMove &&
		columnOf(item) !== column.id &&
		(canMove ? canMove(item, column) : true);

	const found = (id: string | null) =>
		id === null ? undefined : shown.find((item) => idOf(item) === id);

	// A column accepts a drop while something that may enter it is being dragged;
	// with nothing in the air it accepts nothing, so a column is never announced
	// as a target when there is nothing to put in it.
	const accepting = (column: BoardColumn) => {
		const item = found(dragged);
		return item !== undefined && movable(item, column);
	};

	const name = (id: string) => {
		const item = found(id.startsWith(CARD) ? id.slice(CARD.length) : id);
		return item ? labelOf(item) : id;
	};
	const columnName = (id: string) =>
		columns.find((column) => `${COLUMN}${column.id}` === id)?.label ?? id;

	const announcements: Announcements = {
		onDragStart: ({ active }) => `Picked up ${name(String(active.id))}.`,
		onDragOver: ({ active, over }) =>
			over
				? `${name(String(active.id))} will move to ${columnName(String(over.id))}.`
				: `${name(String(active.id))} is over no column and will stay where it is.`,
		onDragEnd: ({ active, over }) =>
			over
				? `${name(String(active.id))} was dropped on ${columnName(String(over.id))}.`
				: `${name(String(active.id))} stayed where it is.`,
		onDragCancel: ({ active }) =>
			`Moving ${name(String(active.id))} was cancelled; it stayed where it is.`,
	};

	// dnd-kit's default instructions promise the space bar and the arrow keys,
	// which belong to a keyboard sensor this board deliberately does not have
	// (see the file comment). Left as they are, they describe to the one reader
	// who depends on them an affordance that is not there, instead of the Move
	// control that is.
	const instructions: ScreenReaderInstructions = {
		draggable:
			"A card can be dragged onto another column with a pointer. Every card that may move also carries a Move control, which lists the columns it may go to.",
	};

	const sensors = useSensors(
		// 5px before a drag begins, so a click on a control inside a card still
		// lands; 250ms on touch, so a swipe still scrolls the page.
		useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
		useSensor(TouchSensor, {
			activationConstraint: { delay: 250, tolerance: 5 },
		}),
	);

	const draggedItem = found(dragged);

	return (
		<div className={cn(className)} style={{ display: "grid", gap: "1rem" }}>
			{searchText && (
				<Input
					type="search"
					aria-label={searchLabel}
					placeholder={searchLabel}
					value={query}
					onChange={(event) => setQuery(event.target.value)}
					style={{ maxWidth: "28rem" }}
				/>
			)}
			<DndContext
				id={contextId}
				sensors={sensors}
				collisionDetection={pointerWithin}
				accessibility={{
					announcements,
					screenReaderInstructions: instructions,
				}}
				onDragStart={({ active }) =>
					setDragged(String(active.id).slice(CARD.length))
				}
				onDragCancel={() => setDragged(null)}
				onDragEnd={({ active, over }) => {
					setDragged(null);
					const item = found(String(active.id).slice(CARD.length));
					const column = columns.find(
						(candidate) => `${COLUMN}${candidate.id}` === String(over?.id),
					);
					if (item && column && movable(item, column)) onMove?.(item, column);
				}}
			>
				<div
					style={{
						display: "grid",
						gap: "1rem",
						alignItems: "start",
						gridTemplateColumns: `repeat(auto-fit, minmax(${columnWidth}, 1fr))`,
					}}
				>
					{columns.map((column) => {
						const cards = byColumn.get(column.id) ?? [];
						return (
							<Column
								key={column.id}
								column={column}
								count={cards.length}
								accepts={accepting(column)}
							>
								<ul style={{ display: "grid", gap: "0.5rem" }}>
									{cards.map((item) => {
										const id = idOf(item);
										const label = labelOf(item);
										const targets = columns.filter((target) =>
											movable(item, target),
										);
										return (
											<Card
												key={id}
												id={id}
												item={item}
												draggable={targets.length > 0}
											>
												{onOpen ? (
													<button
														type="button"
														className="w-full rounded-md p-3 text-left"
														aria-label={`Open ${label}`}
														onClick={() => onOpen(item)}
													>
														{renderCard(item)}
													</button>
												) : (
													<div className="p-3">{renderCard(item)}</div>
												)}
												{targets.length > 0 && (
													<div
														className="border-border border-t"
														style={{ padding: "0.25rem 0.5rem" }}
													>
														<DropdownMenu>
															<DropdownMenuTrigger
																className="rounded-md text-muted-foreground type-card-metadata hover:text-foreground"
																style={{ padding: "0.125rem 0.25rem" }}
															>
																{`Move ${label}`}
															</DropdownMenuTrigger>
															<DropdownMenuContent>
																{targets.map((target) => (
																	<DropdownMenuItem
																		key={target.id}
																		onClick={() => onMove?.(item, target)}
																	>
																		{target.label}
																	</DropdownMenuItem>
																))}
															</DropdownMenuContent>
														</DropdownMenu>
													</div>
												)}
											</Card>
										);
									})}
									{cards.length === 0 && (
										<li
											className="text-center text-muted-foreground type-card-metadata"
											style={{ padding: "1.5rem 0" }}
										>
											{column.empty ?? "Nothing here"}
										</li>
									)}
								</ul>
							</Column>
						);
					})}
				</div>
				{/* The floating copy is the card's contents without its controls: a
				    second button with the same accessible name, mid-drag, would be a
				    second target a reader could reach. */}
				<DragOverlay>
					{draggedItem ? (
						<div
							className="rounded-md border border-border bg-card p-3"
							style={{ cursor: "grabbing" }}
						>
							{renderCard(draggedItem)}
						</div>
					) : null}
				</DragOverlay>
			</DndContext>
		</div>
	);
}
