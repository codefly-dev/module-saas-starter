// Which page numbers a pager shows, as a pure function so the arithmetic can be
// tested without rendering anything. The awkward cases are all at the ends: a
// naive window centred on the current page runs off both edges, and collapsing a
// gap of exactly one page to an ellipsis is worse than printing the page, since
// the ellipsis takes the same room and costs a click.

/** A gap the pager collapses, rather than a page. */
export const PAGE_GAP = "gap" as const;
export type PaginationEntry = number | typeof PAGE_GAP;

export interface PaginationRangeOptions {
	/** 1-based. Clamped into range rather than trusted. */
	page: number;
	pageCount: number;
	/** Pages shown either side of the current one. */
	siblings?: number;
	/** Pages always shown at each end. */
	boundaries?: number;
}

export function paginationRange({
	page,
	pageCount,
	siblings = 1,
	boundaries = 1,
}: PaginationRangeOptions): PaginationEntry[] {
	if (!Number.isFinite(pageCount) || pageCount < 1) return [];
	const total = Math.floor(pageCount);
	const current = Math.min(Math.max(Math.floor(page) || 1, 1), total);
	const edge = Math.max(Math.floor(boundaries), 1);
	const window = Math.max(Math.floor(siblings), 0);

	// Everything fits: never print an ellipsis where the pages themselves would
	// have taken no more room.
	const widest = edge * 2 + window * 2 + 3;
	if (total <= widest)
		return Array.from({ length: total }, (_, index) => index + 1);

	const first = Array.from({ length: edge }, (_, index) => index + 1);
	const last = Array.from(
		{ length: edge },
		(_, index) => total - edge + index + 1,
	);
	const from = Math.max(current - window, edge + 1);
	const to = Math.min(current + window, total - edge);
	const middle: number[] = [];
	for (let value = from; value <= to; value += 1) middle.push(value);

	const entries: PaginationEntry[] = [...first];
	appendGap(entries, first[first.length - 1], middle[0]);
	entries.push(...middle);
	appendGap(entries, middle[middle.length - 1], last[0]);
	entries.push(...last);
	return entries;
}

/**
 * A gap of exactly one page prints that page instead of an ellipsis: the
 * ellipsis is the same width and one more click to reach the page it hides.
 */
function appendGap(
	entries: PaginationEntry[],
	before: number | undefined,
	after: number | undefined,
): void {
	if (before === undefined || after === undefined) return;
	const missing = after - before - 1;
	if (missing <= 0) return;
	if (missing === 1) {
		entries.push(before + 1);
		return;
	}
	entries.push(PAGE_GAP);
}
