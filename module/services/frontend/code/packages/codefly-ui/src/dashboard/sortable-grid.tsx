"use client";

/**
 * SortableGrid — tiles a viewer rearranges by dragging one onto another. The
 * two swap places; no other tile moves.
 *
 * Why a swap: moving a tile into another's place shifts every tile between
 * them, and on a grid that shift wraps across rows, so a sideways drag would
 * move two tiles and a downward one three.
 *
 * While dragging, a floating copy of the tile follows the pointer. Its place in
 * the grid stays dimmed and moves to where the drop will put it, and the tile
 * it will swap with slides into the slot it left. The grid never reflows
 * mid-drag: tiles move by transform over the layout measured when the drag
 * began, so tiles of different heights cannot pull the target out from under
 * the pointer. A drop off the grid, or back on the tile's own slot, changes
 * nothing, and the floating copy glides back.
 *
 * Whenever the order changes (a drop, or the caller moving, adding or removing
 * a tile), every tile glides from where it was drawn to where it lands. That
 * includes tiles the change did not move in the order: swapping a tall tile
 * with a short one changes the height of both rows, which moves the tiles
 * beside them.
 *
 * A mouse drag starts after 5px of movement, so a click on a control inside a
 * tile still lands. A touch drag starts after a 250ms press, so a swipe still
 * scrolls the page. There is no keyboard sensor: the caller gives each tile its
 * keyboard alternative (for instance, a handle whose arrow keys move the tile
 * one place).
 */
import {
	type Announcements,
	type CollisionDetection,
	closestCenter,
	DndContext,
	DragOverlay,
	MouseSensor,
	pointerWithin,
	TouchSensor,
	type UniqueIdentifier,
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
	type ReactNode,
	type SyntheticEvent,
	useId,
	useMemo,
	useState,
} from "react";
import { cn } from "./cn.js";

export type SortableGridProps = {
	/** The tiles' ids, in the order they are shown. */
	ids: readonly string[];
	/** A tile was dropped on another: the dragged tile's id, then the target's. */
	onSwap: (draggedId: string, targetId: string) => void;
	renderItem: (id: string) => ReactNode;
	/** A tile's name in what a screen reader announces during a drag. Defaults to its id. */
	itemLabel?: (id: string) => string;
	/** Classes for the list, which lay the tiles out (for example `grid grid-cols-2 gap-4`). */
	className?: string;
};

// The tile under the pointer. In the gap between tiles it is the nearest one,
// so a drop just beside a tile still lands on it; off the grid there is none.
const tileUnderPointer: CollisionDetection = (args) => {
	const pointer = args.pointerCoordinates;
	if (!pointer) return [];
	const under = pointerWithin(args);
	if (under.length > 0) return under;
	const rects = [...args.droppableRects.values()];
	const onGrid =
		rects.length > 0 &&
		pointer.x >= Math.min(...rects.map((rect) => rect.left)) &&
		pointer.x <= Math.max(...rects.map((rect) => rect.right)) &&
		pointer.y >= Math.min(...rects.map((rect) => rect.top)) &&
		pointer.y <= Math.max(...rects.map((rect) => rect.bottom));
	if (!onGrid) return [];
	return closestCenter({
		...args,
		collisionRect: {
			left: pointer.x,
			right: pointer.x,
			top: pointer.y,
			bottom: pointer.y,
			width: 0,
			height: 0,
		},
	});
};

const GLIDE: KeyframeAnimationOptions = {
	duration: 220,
	easing: "cubic-bezier(0.2, 0, 0, 1)",
};

// Where each tile is drawn now, transforms included.
function drawnRects(list: HTMLElement): Map<string, DOMRect> {
	const rects = new Map<string, DOMRect>();
	for (const tile of list.querySelectorAll<HTMLElement>(
		":scope > [data-sortable-id]",
	)) {
		rects.set(tile.dataset.sortableId ?? "", tile.getBoundingClientRect());
	}
	return rects;
}

function prefersReducedMotion(): boolean {
	return (
		typeof window.matchMedia === "function" &&
		window.matchMedia("(prefers-reduced-motion: reduce)").matches
	);
}

// The tile list. When the order changes it reads where every tile is drawn
// just before React commits the new order, the one moment the page still shows
// the old one, and then glides each tile from there to where it lands. A class
// because getSnapshotBeforeUpdate is React's only way into that moment. (A
// dropped tile is hidden while its floating copy glides into its new slot.)
class GlidingList extends Component<{
	order: string;
	className?: string;
	children: ReactNode;
}> {
	private list = createRef<HTMLUListElement>();

	getSnapshotBeforeUpdate(previous: { order: string }) {
		const list = this.list.current;
		return list && previous.order !== this.props.order
			? drawnRects(list)
			: null;
	}

	componentDidUpdate(
		_previous: unknown,
		_state: unknown,
		before: Map<string, DOMRect> | null,
	) {
		const list = this.list.current;
		if (!before || !list || prefersReducedMotion()) return;
		for (const tile of list.querySelectorAll<HTMLElement>(
			":scope > [data-sortable-id]",
		)) {
			const from = before.get(tile.dataset.sortableId ?? "");
			if (!from) continue;
			const to = tile.getBoundingClientRect();
			const dx = from.left - to.left;
			const dy = from.top - to.top;
			if (Math.abs(dx) < 1 && Math.abs(dy) < 1) continue;
			tile.animate?.(
				[{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }],
				GLIDE,
			);
		}
	}

	render() {
		return (
			<ul ref={this.list} className={this.props.className}>
				{this.props.children}
			</ul>
		);
	}
}

function SortableTile({
	id,
	sorting,
	children,
}: {
	id: string;
	sorting: boolean;
	children: ReactNode;
}) {
	// The list animates order changes itself (see GlidingList), so dnd-kit's
	// own layout animation is off; left on, it would slide the swapped tile back
	// to where the drag began before sliding it on to where it lands.
	const { setNodeRef, listeners, transform, transition, isDragging } =
		useSortable({ id, animateLayoutChanges: () => false });
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

export function SortableGrid({
	ids,
	onSwap,
	renderItem,
	itemLabel = (id) => id,
	className,
}: SortableGridProps) {
	// A stable id keeps the ids dnd-kit writes into the page the same on the
	// server and the client.
	const contextId = useId();
	const [dragged, setDragged] = useState<string | null>(null);
	const items = useMemo(() => [...ids], [ids]);

	const sensors = useSensors(
		useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
		useSensor(TouchSensor, {
			activationConstraint: { delay: 250, tolerance: 5 },
		}),
	);
	const name = (id: UniqueIdentifier) => itemLabel(String(id));
	const announcements: Announcements = {
		onDragStart: ({ active }) => `Picked up ${name(active.id)}.`,
		onDragOver: ({ active, over }) =>
			over && over.id !== active.id
				? `${name(active.id)} will swap with ${name(over.id)}.`
				: `${name(active.id)} will stay where it is.`,
		onDragEnd: ({ active, over }) =>
			over && over.id !== active.id
				? `${name(active.id)} swapped with ${name(over.id)}.`
				: `${name(active.id)} stayed where it is.`,
		onDragCancel: ({ active }) =>
			`Moving ${name(active.id)} was cancelled; it stayed where it is.`,
	};

	return (
		<DndContext
			id={contextId}
			sensors={sensors}
			collisionDetection={tileUnderPointer}
			accessibility={{ announcements }}
			onDragStart={({ active }) => setDragged(String(active.id))}
			onDragEnd={({ active, over }) => {
				setDragged(null);
				if (over && over.id !== active.id) {
					onSwap(String(active.id), String(over.id));
				}
			}}
			onDragCancel={() => setDragged(null)}
		>
			<SortableContext items={items} strategy={rectSwappingStrategy}>
				<GlidingList order={JSON.stringify(ids)} className={className}>
					{ids.map((id) => (
						<SortableTile key={id} id={id} sorting={dragged !== null}>
							{renderItem(id)}
						</SortableTile>
					))}
				</GlidingList>
			</SortableContext>
			{/* On a drop the copy glides into the tile's new slot, or back to its
			    old one when there was nothing to swap with. */}
			<DragOverlay>
				{dragged ? (
					<div className="cursor-grabbing drop-shadow-lg">
						{renderItem(dragged)}
					</div>
				) : null}
			</DragOverlay>
		</DndContext>
	);
}
