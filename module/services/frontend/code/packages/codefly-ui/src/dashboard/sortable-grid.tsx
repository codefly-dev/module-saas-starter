"use client";

/**
 * SortableBoard — tiles a viewer rearranges by dragging, in one or more
 * groups (for instance the sections of a dashboard). A tile dropped on another
 * swaps places with it, in the same group or another; no other tile moves.
 * SortableGrid is the board with a single group.
 *
 * Why a swap: moving a tile into another's place shifts every tile between
 * them, and on a grid that shift wraps across rows, so a sideways drag would
 * move two tiles and a downward one three.
 *
 * While dragging, a floating copy of the tile follows the pointer. Its place on
 * the board stays dimmed and moves to where the drop will put it, and the tile
 * it will swap with slides into the place it left. The board never reflows
 * mid-drag: tiles move by transform over the layout measured when the drag
 * began, so tiles of different heights cannot pull the target out from under
 * the pointer. A drop off the board, or back on the tile's own place, changes
 * nothing, and the floating copy glides back.
 *
 * A board that takes `onMove` also lets a tile go to the end of any group:
 * while a drag is in flight, every group ends with a dashed, tile-sized slot,
 * and a drop on it moves the tile there. The slots exist only during a drag.
 *
 * A board that takes `onMoveGroup` also lets the viewer reorder the groups. A
 * group is dragged by a handle its header spreads (for instance a grip beside
 * its title), with its preview following the pointer. The groups are one
 * column, so a drop inserts rather than swaps: on the top half of a group the
 * dragged one goes before it, on the bottom half after it, and a line marks
 * the place meanwhile. Only the height counts: above the first group is before
 * it, below the last after it. During a group's drag no tile is a drop target
 * and there is no slot. A `fixed` group cannot be dragged, and no group lands
 * on it: one dropped there goes to the nearest place a group can go.
 *
 * Whenever the order changes (a drop, or the caller moving, adding or removing
 * a tile or a group), every tile and group glides from where it was drawn to
 * where it lands, from one group to another too. That includes tiles the
 * change did not move in the order: swapping a tall tile with a short one
 * changes the height of both rows, which moves the tiles beside them.
 *
 * A mouse drag starts after 5px of movement, so a click on a control inside a
 * tile still lands. A touch drag starts after a 250ms press, so a swipe still
 * scrolls the page. There is no keyboard sensor: the caller gives each tile its
 * keyboard alternative (for instance, a handle whose arrow keys move the tile
 * one place).
 */
import {
	type Active,
	type Announcements,
	type CollisionDetection,
	closestCenter,
	DndContext,
	DragOverlay,
	type DroppableContainer,
	MouseSensor,
	type Over,
	pointerWithin,
	TouchSensor,
	type UniqueIdentifier,
	useDndContext,
	useDraggable,
	useDroppable,
	useSensor,
	useSensors,
} from "@dnd-kit/core";
import {
	rectSwappingStrategy,
	SortableContext,
	useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
	Component,
	createRef,
	type DOMAttributes,
	type ReactNode,
	type SyntheticEvent,
	useCallback,
	useId,
	useMemo,
	useState,
} from "react";
import { cn } from "./cn.js";

/**
 * What a group is dragged by: spread it on the element that starts the drag,
 * for instance a grip in the group's header. It carries the pointer and touch
 * handlers only; the element's keyboard alternative is the caller's.
 */
export type SortableGroupHandle = Pick<
	DOMAttributes<HTMLElement>,
	"onMouseDown" | "onTouchStart"
>;

/** One group of tiles on a board. */
export type SortableGroup = {
	/** The group's id, which `onMove` and `onMoveGroup` hand back. */
	id: string;
	/** The group's tiles' ids, in the order they are shown. */
	ids: readonly string[];
	/**
	 * Drawn above the group's tiles, for instance its heading. On a board that
	 * takes `onMoveGroup`, a function of the handle to spread on what drags
	 * the group.
	 */
	header?: ReactNode | ((handle: SortableGroupHandle) => ReactNode);
	/** What follows the pointer while the group is dragged, for instance its title. Defaults to its label. */
	preview?: ReactNode;
	/** A group that stays where it is: it cannot be dragged, and a group dropped on it goes to the nearest place a group can go. */
	fixed?: boolean;
	/** Classes for the group's list, which lay its tiles out (for example `grid grid-cols-2 gap-4`). */
	className?: string;
	/** The group's name in what a screen reader announces. Defaults to its id. */
	label?: string;
};

export type SortableBoardProps = {
	/** The groups, in the order they are shown. A tile id appears in one group only. */
	groups: readonly SortableGroup[];
	/** A tile was dropped on another: the dragged tile's id, then the target's. */
	onSwap: (draggedId: string, targetId: string) => void;
	/**
	 * A tile was dropped on a group's slot: move it to the end of that group.
	 * Without it, the board offers no slot.
	 */
	onMove?: (draggedId: string, groupId: string) => void;
	/**
	 * A group was dragged by its handle and dropped before or after another:
	 * move it to `index`, its place in the new order. Without it, groups stay
	 * where they are.
	 */
	onMoveGroup?: (groupId: string, index: number) => void;
	renderItem: (id: string) => ReactNode;
	/**
	 * What the floating copy shows while a tile is dragged. Defaults to
	 * `renderItem`. Give a lighter copy when a tile holds controls or panels
	 * (a menu, a popover) that should not exist twice on the page mid-drag.
	 */
	renderOverlay?: (id: string) => ReactNode;
	/** A tile's name in what a screen reader announces during a drag. Defaults to its id. */
	itemLabel?: (id: string) => string;
	/**
	 * Classes for one tile's list item, which is its group's grid item: for
	 * instance `row-span-2` for a tile about twice as tall as the others.
	 */
	itemClassName?: (id: string) => string | undefined;
	/** Classes for the board, which lay the groups out (for example `space-y-6`). */
	className?: string;
};

export type SortableGridProps = Omit<
	SortableBoardProps,
	"groups" | "onMove" | "onMoveGroup" | "className"
> & {
	/** The tiles' ids, in the order they are shown. */
	ids: readonly string[];
	/** Classes for the list, which lay the tiles out (for example `grid grid-cols-2 gap-4`). */
	className?: string;
};

// What the board keeps on each draggable and droppable: the group it belongs
// to, and what it is. A tile; a group's slot; a group being dragged; or one
// half of a group, where a dragged group lands before or after it.
type Place = {
	group: string;
	kind: "tile" | "slot" | "group" | "before" | "after";
};

function placeOf(target: { data: { current?: unknown } }): Place | undefined {
	return target.data.current as Place | undefined;
}

// The group a drag is moving, when it moves a group rather than a tile.
function movingGroup(active: Active): string | undefined {
	const place = placeOf(active);
	return place?.kind === "group" ? place.group : undefined;
}

type Bounds = { left: number; right: number; top: number; bottom: number };

function bounds(rects: readonly Bounds[]): Bounds {
	return {
		left: Math.min(...rects.map((rect) => rect.left)),
		right: Math.max(...rects.map((rect) => rect.right)),
		top: Math.min(...rects.map((rect) => rect.top)),
		bottom: Math.max(...rects.map((rect) => rect.bottom)),
	};
}

// The tile under the pointer. In the gap between a group's tiles it is the
// group's nearest one (or its slot), so a drop just beside a tile still lands
// on it; off every group there is none, and a group's header is off it.
const tileUnderPointer: CollisionDetection = (args) => {
	const pointer = args.pointerCoordinates;
	if (!pointer) return [];
	const under = pointerWithin(args);
	if (under.length > 0) return under;
	const byGroup = new Map<string | undefined, DroppableContainer[]>();
	for (const container of args.droppableContainers) {
		const group = placeOf(container)?.group;
		byGroup.set(group, [...(byGroup.get(group) ?? []), container]);
	}
	for (const containers of byGroup.values()) {
		const rects = containers.flatMap((container) => {
			const rect = args.droppableRects.get(container.id);
			return rect ? [rect] : [];
		});
		if (rects.length === 0) continue;
		const group = bounds(rects);
		if (
			pointer.x < group.left ||
			pointer.x > group.right ||
			pointer.y < group.top ||
			pointer.y > group.bottom
		) {
			continue;
		}
		return closestCenter({
			...args,
			droppableContainers: containers,
			collisionRect: {
				left: pointer.x,
				right: pointer.x,
				top: pointer.y,
				bottom: pointer.y,
				width: 0,
				height: 0,
			},
		});
	}
	return [];
};

// Where a dragged group lands: the half of a group nearest the pointer's
// height, the top half for before it and the bottom half for after it. The
// groups are one column, so only the height counts: between two groups either
// nearest half is the same place, above the first group is before it (the
// page's own header, where a viewer drags a group to put it first), and below
// the last is after it. A drag the viewer does not want lands back on its own
// place, or is cancelled with Escape.
const halfUnderPointer: CollisionDetection = ({
	pointerCoordinates: pointer,
	droppableContainers,
	droppableRects,
}) => {
	if (!pointer) return [];
	const halves = droppableContainers.flatMap((container) => {
		const rect = droppableRects.get(container.id);
		if (!rect) return [];
		const middle = rect.top + rect.height / 2;
		const before = placeOf(container)?.kind === "before";
		const top = before ? rect.top : middle;
		const bottom = before ? middle : rect.bottom;
		const distance = Math.max(0, top - pointer.y, pointer.y - bottom);
		return [{ container, distance }];
	});
	if (halves.length === 0) return [];
	const [nearest] = halves.sort((a, b) => a.distance - b.distance);
	return [
		{
			id: nearest.container.id,
			data: { droppableContainer: nearest.container, value: nearest.distance },
		},
	];
};

// A tile's drag lands on a tile or a slot, a group's drag on a group's half.
const landing: CollisionDetection = (args) => {
	const group = movingGroup(args.active) !== undefined;
	const droppableContainers = args.droppableContainers.filter((container) => {
		const kind = placeOf(container)?.kind;
		return group
			? kind === "before" || kind === "after"
			: kind === "tile" || kind === "slot";
	});
	return group
		? halfUnderPointer({ ...args, droppableContainers })
		: tileUnderPointer({ ...args, droppableContainers });
};

const GLIDE: KeyframeAnimationOptions = {
	duration: 220,
	easing: "cubic-bezier(0.2, 0, 0, 1)",
};

// The board's groups, and their tiles, but not a nested board's.
const GROUPS = ":scope > [data-sortable-group]";
const TILES = `${GROUPS} > ul > [data-sortable-id]`;

type Drawn = { groups: Map<string, DOMRect>; tiles: Map<string, DOMRect> };

// Where each group and tile is drawn now, transforms included.
function drawnRects(board: HTMLElement): Drawn {
	const drawn: Drawn = { groups: new Map(), tiles: new Map() };
	for (const group of board.querySelectorAll<HTMLElement>(GROUPS)) {
		drawn.groups.set(
			group.dataset.sortableGroup ?? "",
			group.getBoundingClientRect(),
		);
	}
	for (const tile of board.querySelectorAll<HTMLElement>(TILES)) {
		drawn.tiles.set(
			tile.dataset.sortableId ?? "",
			tile.getBoundingClientRect(),
		);
	}
	return drawn;
}

function glide(element: HTMLElement, dx: number, dy: number) {
	if (Math.abs(dx) < 1 && Math.abs(dy) < 1) return;
	element.animate?.(
		[{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }],
		GLIDE,
	);
}

function prefersReducedMotion(): boolean {
	return (
		typeof window.matchMedia === "function" &&
		window.matchMedia("(prefers-reduced-motion: reduce)").matches
	);
}

// The board. When the order changes it reads where every group and tile is
// drawn just before React commits the new order, the one moment the page still
// shows the old one, and then glides each from there to where it lands. It
// reads the whole board, keyed by id, so a tile that moved to another group
// (and so is a new element there) glides too. A tile glides with its group, so
// on its own it glides only by what it moved within the group. A class because
// getSnapshotBeforeUpdate is React's only way into that moment. (A dropped
// tile, or a dropped group's header, is hidden while its floating copy glides
// into its new place.)
class GlidingBoard extends Component<{
	order: string;
	className?: string;
	children: ReactNode;
}> {
	private board = createRef<HTMLDivElement>();

	getSnapshotBeforeUpdate(previous: { order: string }) {
		const board = this.board.current;
		return board && previous.order !== this.props.order
			? drawnRects(board)
			: null;
	}

	componentDidUpdate(
		_previous: unknown,
		_state: unknown,
		before: Drawn | null,
	) {
		const board = this.board.current;
		if (!before || !board || prefersReducedMotion()) return;
		// Everything is read before anything glides: a group's glide moves
		// its tiles with it.
		const after = drawnRects(board);
		const moved = new Map<string, { dx: number; dy: number }>();
		for (const group of board.querySelectorAll<HTMLElement>(GROUPS)) {
			const id = group.dataset.sortableGroup ?? "";
			const from = before.groups.get(id);
			const to = after.groups.get(id);
			if (!from || !to) continue;
			const delta = { dx: from.left - to.left, dy: from.top - to.top };
			moved.set(id, delta);
			glide(group, delta.dx, delta.dy);
		}
		for (const tile of board.querySelectorAll<HTMLElement>(TILES)) {
			const id = tile.dataset.sortableId ?? "";
			const from = before.tiles.get(id);
			const to = after.tiles.get(id);
			if (!from || !to) continue;
			const group = moved.get(
				tile.parentElement?.parentElement?.dataset.sortableGroup ?? "",
			);
			glide(
				tile,
				from.left - to.left - (group?.dx ?? 0),
				from.top - to.top - (group?.dy ?? 0),
			);
		}
	}

	render() {
		return (
			<div ref={this.board} className={this.props.className}>
				{this.props.children}
			</div>
		);
	}
}

function SortableTile({
	id,
	group,
	sorting,
	className,
	children,
}: {
	id: string;
	group: string;
	sorting: boolean;
	className?: string;
	children: ReactNode;
}) {
	// The board animates order changes itself (see GlidingBoard), so dnd-kit's
	// own layout animation is off; left on, it would slide the swapped tile back
	// to where the drag began before sliding it on to where it lands.
	const { setNodeRef, listeners, transform, transition, isDragging } =
		useSortable({
			id,
			data: { group, kind: "tile" } satisfies Place,
			animateLayoutChanges: () => false,
		});
	// React passes a press inside a portal (a popover or menu opened from the
	// tile) up through the tile, though the portal is drawn elsewhere on the
	// page. Only a press on the tile itself starts a drag, so text in such a
	// panel can still be selected.
	const pressListeners = useMemo(
		() =>
			Object.fromEntries(
				Object.entries(listeners ?? {}).map(([name, handler]) => [
					name,
					(event: SyntheticEvent) => {
						if (event.currentTarget.contains(event.target as Node)) {
							handler(event);
						}
					},
				]),
			),
		[listeners],
	);
	return (
		<li
			ref={setNodeRef}
			{...pressListeners}
			data-sortable-id={id}
			data-dragging={isDragging || undefined}
			className={cn(
				"cursor-grab touch-manipulation",
				isDragging && "opacity-40",
				className,
			)}
			// A CSS transition outranks the glide animation, so one is set only
			// while a drag is sliding tiles around.
			style={{
				transform: CSS.Translate.toString(transform),
				transition: sorting ? transition : undefined,
			}}
		>
			{children}
		</li>
	);
}

// The end of a group, while a drag is in flight: a drop here moves the tile to
// the end of the group. As big as the tile being dragged, so the drop target
// reads as a place the tile will fit.
function MoveSlot({ id, group }: { id: string; group: string }) {
	const { setNodeRef, isOver } = useDroppable({
		id,
		data: { group, kind: "slot" } satisfies Place,
	});
	const { activeNodeRect } = useDndContext();
	return (
		<li
			ref={setNodeRef}
			data-sortable-slot={group}
			className={cn(
				"flex items-center justify-center rounded-xl border-2 border-dashed border-foreground/20 type-caption-plain text-muted-foreground",
				isOver && "border-primary bg-primary/5 text-foreground",
			)}
			style={{ minHeight: activeNodeRect?.height }}
		>
			Move here
		</li>
	);
}

// One group on the board. On a board that moves groups, the group is dragged
// by the handle its header spreads (the header is what the drag lifts, so the
// floating copy is its size), and each half of the group is where a dragged
// group lands: the top half before it, the bottom half after it.
function SortableGroupView({
	group,
	boardId,
	movable,
	line,
	children,
}: {
	group: SortableGroup;
	boardId: string;
	movable: boolean;
	line?: "before" | "after";
	children: ReactNode;
}) {
	const {
		setNodeRef: setLifted,
		listeners,
		isDragging,
	} = useDraggable({
		id: `${boardId}:group:${group.id}`,
		data: { group: group.id, kind: "group" } satisfies Place,
		disabled: !movable,
	});
	const { setNodeRef: setBefore } = useDroppable({
		id: `${boardId}:before:${group.id}`,
		data: { group: group.id, kind: "before" } satisfies Place,
		disabled: !movable,
	});
	const { setNodeRef: setAfter } = useDroppable({
		id: `${boardId}:after:${group.id}`,
		data: { group: group.id, kind: "after" } satisfies Place,
		disabled: !movable,
	});
	const setHalves = useCallback(
		(element: HTMLElement | null) => {
			setBefore(element);
			setAfter(element);
		},
		[setBefore, setAfter],
	);
	// The board's sensors are a mouse's and a touch's, so these two are all
	// the listeners there are.
	const { onMouseDown, onTouchStart } = (listeners ??
		{}) as SortableGroupHandle;
	const header =
		typeof group.header === "function"
			? group.header({ onMouseDown, onTouchStart })
			: group.header;
	return (
		<div
			ref={setHalves}
			data-sortable-group={group.id}
			data-dragging={isDragging || undefined}
			className={cn("relative", isDragging && "opacity-40")}
		>
			{line && (
				<div
					aria-hidden
					data-sortable-line={line}
					className={cn(
						"pointer-events-none absolute inset-x-0 h-0.5 rounded-full bg-primary",
						line === "before" ? "-top-1.5" : "-bottom-1.5",
					)}
				/>
			)}
			{header !== undefined && (
				<div ref={setLifted} data-sortable-group-header="">
					{header}
				</div>
			)}
			<ul className={group.className}>{children}</ul>
		</div>
	);
}

type Dragged = { kind: "tile" | "group"; id: string };

export function SortableBoard({
	groups,
	onSwap,
	onMove,
	onMoveGroup,
	renderItem,
	renderOverlay = renderItem,
	itemLabel = (id) => id,
	itemClassName,
	className,
}: SortableBoardProps) {
	// A stable id keeps the ids dnd-kit writes into the page the same on the
	// server and the client.
	const contextId = useId();
	const [dragged, setDragged] = useState<Dragged | null>(null);
	// Where a dragged group would land, to mark the place with a line.
	const [overPlace, setOverPlace] = useState<Place | null>(null);
	// Callers usually pass new arrays on every render, so the order is keyed by
	// its content: dnd-kit sees a new items list only when the order changes.
	// One sortable context over every group, so a tile previews its swap with
	// a tile in another group the way it does with one in its own.
	const order = JSON.stringify(groups.map((group) => [group.id, group.ids]));
	const items = useMemo(
		() => (JSON.parse(order) as [string, string[]][]).flatMap(([, ids]) => ids),
		[order],
	);
	// A slot's id is scoped to this board, so it cannot collide with a tile's.
	const slotId = (group: string) => `${contextId}:slot:${group}`;

	const sensors = useSensors(
		useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
		useSensor(TouchSensor, {
			activationConstraint: { delay: 250, tolerance: 5 },
		}),
	);
	const name = (id: UniqueIdentifier) => itemLabel(String(id));
	const groupOf = (id: UniqueIdentifier) =>
		groups.find((group) => group.ids.includes(String(id)));
	const groupName = (id: string) =>
		groups.find((group) => group.id === id)?.label ?? id;
	// Where a tile dropped on `over` lands: a group's end, the other tile's
	// place (naming that group when it is another one), or nowhere.
	const outcome = (
		active: UniqueIdentifier,
		over: Over | null,
	):
		| { kind: "move"; group: string }
		| { kind: "swap"; target: string; group?: string }
		| null => {
		if (!over) return null;
		const place = placeOf(over);
		if (place?.kind === "slot") {
			const ids = groups.find((group) => group.id === place.group)?.ids ?? [];
			return ids[ids.length - 1] === String(active)
				? null
				: { kind: "move", group: place.group };
		}
		if (place?.kind !== "tile" || over.id === active) return null;
		const target = groupOf(over.id);
		return {
			kind: "swap",
			target: String(over.id),
			group: target && target !== groupOf(active) ? target.id : undefined,
		};
	};
	// Where a group dropped on a group's half lands: its place in the new
	// order, before or after that group, or nowhere when that is where it is.
	const groupOutcome = (
		moving: string,
		place: Place | null | undefined,
	): { target: string; side: "before" | "after"; index: number } | null => {
		if (place?.kind !== "before" && place?.kind !== "after") return null;
		const order = groups.map((group) => group.id);
		const from = order.indexOf(moving);
		const at = order.indexOf(place.group);
		if (from === -1 || at === -1) return null;
		let index = place.kind === "after" ? at + 1 : at;
		if (from < index) index -= 1;
		return index === from
			? null
			: { target: place.group, side: place.kind, index };
	};
	const stays = (id: UniqueIdentifier) => `${name(id)} will stay where it is.`;
	const announcements: Announcements = {
		onDragStart: ({ active }) => {
			const moving = movingGroup(active);
			return `Picked up ${moving === undefined ? name(active.id) : groupName(moving)}.`;
		},
		onDragOver: ({ active, over }) => {
			const moving = movingGroup(active);
			if (moving !== undefined) {
				const next = groupOutcome(moving, over && placeOf(over));
				return next
					? `${groupName(moving)} will move ${next.side} ${groupName(next.target)}.`
					: `${groupName(moving)} will stay where it is.`;
			}
			const next = outcome(active.id, over);
			if (!next) return stays(active.id);
			if (next.kind === "move") {
				return `${name(active.id)} will move to the end of ${groupName(next.group)}.`;
			}
			return next.group
				? `${name(active.id)} will swap with ${name(next.target)} and move to ${groupName(next.group)}.`
				: `${name(active.id)} will swap with ${name(next.target)}.`;
		},
		onDragEnd: ({ active, over }) => {
			const moving = movingGroup(active);
			if (moving !== undefined) {
				const next = groupOutcome(moving, over && placeOf(over));
				return next
					? `${groupName(moving)} moved ${next.side} ${groupName(next.target)}.`
					: `${groupName(moving)} stayed where it is.`;
			}
			const next = outcome(active.id, over);
			if (!next) return `${name(active.id)} stayed where it is.`;
			if (next.kind === "move") {
				return `${name(active.id)} moved to the end of ${groupName(next.group)}.`;
			}
			return next.group
				? `${name(active.id)} swapped with ${name(next.target)} and moved to ${groupName(next.group)}.`
				: `${name(active.id)} swapped with ${name(next.target)}.`;
		},
		onDragCancel: ({ active }) => {
			const moving = movingGroup(active);
			return `Moving ${moving === undefined ? name(active.id) : groupName(moving)} was cancelled; it stayed where it is.`;
		},
	};
	const landingGroup =
		dragged?.kind === "group" ? groupOutcome(dragged.id, overPlace) : null;
	const liftedGroup =
		dragged?.kind === "group"
			? groups.find((group) => group.id === dragged.id)
			: undefined;

	return (
		<DndContext
			id={contextId}
			sensors={sensors}
			collisionDetection={landing}
			accessibility={{ announcements }}
			onDragStart={({ active }) => {
				const moving = movingGroup(active);
				setDragged(
					moving === undefined
						? { kind: "tile", id: String(active.id) }
						: { kind: "group", id: moving },
				);
			}}
			onDragOver={({ over }) => setOverPlace((over && placeOf(over)) ?? null)}
			onDragEnd={({ active, over }) => {
				setDragged(null);
				setOverPlace(null);
				const moving = movingGroup(active);
				if (moving !== undefined) {
					const next = groupOutcome(moving, over && placeOf(over));
					if (next) onMoveGroup?.(moving, next.index);
					return;
				}
				const next = outcome(active.id, over);
				if (next?.kind === "move") onMove?.(String(active.id), next.group);
				else if (next?.kind === "swap") onSwap(String(active.id), next.target);
			}}
			onDragCancel={() => {
				setDragged(null);
				setOverPlace(null);
			}}
		>
			<SortableContext items={items} strategy={rectSwappingStrategy}>
				<GlidingBoard order={order} className={className}>
					{groups.map((group) => (
						<SortableGroupView
							key={group.id}
							group={group}
							boardId={contextId}
							movable={onMoveGroup !== undefined && !group.fixed}
							line={
								landingGroup?.target === group.id
									? landingGroup.side
									: undefined
							}
						>
							{group.ids.map((id) => (
								<SortableTile
									key={id}
									id={id}
									group={group.id}
									sorting={dragged?.kind === "tile"}
									className={itemClassName?.(id)}
								>
									{renderItem(id)}
								</SortableTile>
							))}
							{onMove && dragged?.kind === "tile" && (
								<MoveSlot id={slotId(group.id)} group={group.id} />
							)}
						</SortableGroupView>
					))}
				</GlidingBoard>
			</SortableContext>
			{/* On a drop the copy glides into the tile's new place, or back to its
			    old one when there was nothing to swap with; a group's copy is
			    its preview, and glides onto its header. */}
			<DragOverlay>
				{dragged?.kind === "tile" ? (
					<div className="cursor-grabbing drop-shadow-lg">
						{renderOverlay(dragged.id)}
					</div>
				) : liftedGroup ? (
					<div className="cursor-grabbing drop-shadow-lg">
						{liftedGroup.preview ?? liftedGroup.label ?? liftedGroup.id}
					</div>
				) : null}
			</DragOverlay>
		</DndContext>
	);
}

// The single group of a SortableGrid.
const GRID = "tiles";

export function SortableGrid({ ids, className, ...board }: SortableGridProps) {
	return <SortableBoard groups={[{ id: GRID, ids, className }]} {...board} />;
}
