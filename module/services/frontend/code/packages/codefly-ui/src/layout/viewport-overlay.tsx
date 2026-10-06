"use client";

export type ViewportRegion =
	| { kind: "rect"; x: number; y: number; width: number; height: number }
	| { kind: "polygon"; points: readonly (readonly [number, number])[] };

export type ViewportOverlayAttributes = Record<`data-${string}`, string>;
export type ViewportOverlayItem = {
	id: string;
	regions: readonly ViewportRegion[];
	stroke: string;
	fill: string;
	strokeWidth?: number;
	dashed?: boolean;
	attributes?: ViewportOverlayAttributes;
};
export type ViewportOverlayProps = {
	items: readonly ViewportOverlayItem[];
	attributes?: ViewportOverlayAttributes;
};

/** Decorations in the positioned parent's CSS-pixel coordinate frame. */
export function ViewportOverlay({ items, attributes }: ViewportOverlayProps) {
	return (
		<div
			{...attributes}
			aria-hidden
			style={{
				position: "absolute",
				inset: 0,
				pointerEvents: "none",
				overflow: "hidden",
			}}
		>
			{items.flatMap(
				({
					id,
					regions,
					stroke,
					fill,
					strokeWidth = 2,
					dashed = false,
					attributes: shapeAttributes,
				}) =>
					regions.map((region, index) =>
						region.kind === "rect" ? (
							<div
								// biome-ignore lint/suspicious/noArrayIndexKey: Regions are stateless decorations within a stable item.
								key={`${id}:${index}`}
								{...shapeAttributes}
								style={{
									position: "absolute",
									left: region.x,
									top: region.y,
									width: region.width,
									height: region.height,
									outlineWidth: strokeWidth,
									outlineStyle: dashed ? "dashed" : "solid",
									outlineColor: stroke,
									outlineOffset: -1,
									background: fill,
									borderRadius: 2,
								}}
							/>
						) : (
							<svg
								aria-hidden="true"
								// biome-ignore lint/suspicious/noArrayIndexKey: Regions are stateless decorations within a stable item.
								key={`${id}:${index}`}
								{...shapeAttributes}
								style={{
									position: "absolute",
									inset: 0,
									width: "100%",
									height: "100%",
									overflow: "visible",
								}}
							>
								<polygon
									points={region.points.map(([x, y]) => `${x},${y}`).join(" ")}
									fill={fill}
									stroke={stroke}
									strokeWidth={strokeWidth}
									strokeDasharray={dashed ? "6 4" : undefined}
								/>
							</svg>
						),
					),
			)}
		</div>
	);
}
