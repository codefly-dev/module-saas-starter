/**
 * Chart geometry — pure, DOM-free helpers shared by the line/area/bar
 * components. Kept separate so the maths (scales, nice ticks, path building)
 * is unit-testable without rendering, and so the charts stay thin.
 *
 * Why hand-rolled SVG rather than a charting library: the dashboard needs a
 * handful of small metric charts, not a plotting engine. A pure-SVG kit built
 * on these helpers renders on the server, ships no runtime, and inherits the
 * appearance tokens (`--chart-1…5`, light/dark) for free — the same reasoning
 * that keeps `sparkline.tsx` library-free, scaled up to axes and multiple
 * series.
 */

export interface ChartDatum {
	/** X position — a time bucket or dimension key (e.g. a day or event type). */
	label: string;
	/** Y magnitude. Charts consume metric values, never raw RPC rows. */
	value: number;
}

export interface ChartSeries {
	/** Identity used by the legend, tooltip, and accessible table. */
	name: string;
	/**
	 * Points in x order. Multiple series are aligned by index against a shared
	 * label axis (`data[i].label` of the first series names bucket `i`).
	 */
	data: ChartDatum[];
}

export interface Point {
	x: number;
	y: number;
}

export interface ResolvedSeries {
	name: string;
	/**
	 * Values aligned index-for-index to the shared label axis from
	 * {@link unionLabels}. `null` marks a label this series has no point for (a
	 * gap) or a non-finite value that was dropped at ingestion.
	 */
	values: (number | null)[];
}

/**
 * The ordered label axis shared by every series, as the union of all series'
 * labels in first-seen order. Series are aligned to *this* axis by label rather
 * than by raw array index, so a chart of separately-aggregated metrics — whose
 * bucket sets routinely differ (a time bucket with no events is simply absent)
 * — plots each value under its own label instead of crashing or silently
 * shifting values onto the wrong bucket.
 */
export function unionLabels(series: ChartSeries[]): string[] {
	const seen = new Set<string>();
	const labels: string[] = [];
	for (const s of series) {
		for (const d of s.data) {
			if (!seen.has(d.label)) {
				seen.add(d.label);
				labels.push(d.label);
			}
		}
	}
	return labels;
}

/**
 * Projects each series onto the shared label axis. A label the series lacks
 * becomes `null`; a non-finite value (NaN/±Infinity from e.g. a rate over an
 * empty denominator) is dropped to `null` here, at the boundary, so it never
 * reaches the scales and produces `NaN` geometry.
 */
export function resolveSeries(
	series: ChartSeries[],
	labels: string[],
): ResolvedSeries[] {
	return series.map((s) => {
		const byLabel = new Map<string, number>();
		for (const d of s.data) {
			if (Number.isFinite(d.value)) byLabel.set(d.label, d.value);
		}
		return {
			name: s.name,
			values: labels.map((label) => byLabel.get(label) ?? null),
		};
	});
}

/**
 * Value domain across every resolved series, always anchored at zero. A bar or
 * area whose baseline is not zero misreports magnitude (the classic
 * truncated-axis lie), so zero is included even when all values are positive;
 * negative values extend the domain downward to whatever the data needs.
 */
export function valuesExtent(series: ResolvedSeries[]): [number, number] {
	let min = 0;
	let max = 0;
	for (const s of series) {
		for (const v of s.values) {
			if (v === null) continue;
			if (v < min) min = v;
			if (v > max) max = v;
		}
	}
	// A flat all-zero (or empty) series still needs a non-degenerate range.
	if (min === 0 && max === 0) return [0, 1];
	return [min, max];
}

function niceNum(range: number, round: boolean): number {
	const exponent = Math.floor(Math.log10(range));
	const fraction = range / 10 ** exponent;
	let niceFraction: number;
	if (round) {
		if (fraction < 1.5) niceFraction = 1;
		else if (fraction < 3) niceFraction = 2;
		else if (fraction < 7) niceFraction = 5;
		else niceFraction = 10;
	} else {
		if (fraction <= 1) niceFraction = 1;
		else if (fraction <= 2) niceFraction = 2;
		else if (fraction <= 5) niceFraction = 5;
		else niceFraction = 10;
	}
	return niceFraction * 10 ** exponent;
}

/**
 * Rounded axis ticks spanning [min, max] with roughly `count` steps landing on
 * 1/2/5·10ⁿ boundaries. The returned array's first and last entries are the
 * padded domain the axis should scale into, so gridlines and the plot align.
 */
export function niceTicks(min: number, max: number, count = 5): number[] {
	if (!(max > min)) return [min];
	const step = niceNum(
		niceNum(max - min, false) / Math.max(1, count - 1),
		true,
	);
	const start = Math.floor(min / step) * step;
	const end = Math.ceil(max / step) * step;
	const ticks: number[] = [];
	// Guard the loop bound against floating-point dust that would otherwise
	// drop or duplicate the final tick.
	for (let value = start; value <= end + step / 2; value += step) {
		const rounded = Number(value.toFixed(10));
		ticks.push(Object.is(rounded, -0) ? 0 : rounded);
	}
	return ticks;
}

/** Linear map from a value domain onto a pixel range. */
export function linearScale(
	domainMin: number,
	domainMax: number,
	rangeMin: number,
	rangeMax: number,
): (value: number) => number {
	const domainSpan = domainMax - domainMin || 1;
	const rangeSpan = rangeMax - rangeMin;
	return (value) => rangeMin + ((value - domainMin) / domainSpan) * rangeSpan;
}

/**
 * X pixel centers for `count` points spread across [left, right]. Used as a
 * point scale for line/area (first point on the left edge, last on the right)
 * and — with `band` — as band centers for bars (each inset half a slot).
 */
export function axisPositions(
	count: number,
	left: number,
	right: number,
	band = false,
): number[] {
	if (count <= 0) return [];
	const width = right - left;
	if (band) {
		const slot = width / count;
		return Array.from({ length: count }, (_, i) => left + (i + 0.5) * slot);
	}
	if (count === 1) return [left + width / 2];
	const step = width / (count - 1);
	return Array.from({ length: count }, (_, i) => left + i * step);
}

/** `M x,y L x,y …` through the points, in order. */
export function linePath(points: Point[]): string {
	return points
		.map((p, i) => `${i === 0 ? "M" : "L"}${p.x.toFixed(2)},${p.y.toFixed(2)}`)
		.join(" ");
}

/** Closed path under a line down to `baselineY`, for an area fill. */
export function areaPath(points: Point[], baselineY: number): string {
	if (points.length === 0) return "";
	const first = points[0];
	const last = points[points.length - 1];
	return `${linePath(points)} L${last.x.toFixed(2)},${baselineY.toFixed(2)} L${first.x.toFixed(2)},${baselineY.toFixed(2)} Z`;
}
