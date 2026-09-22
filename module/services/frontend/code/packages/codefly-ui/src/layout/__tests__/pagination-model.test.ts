import { describe, expect, it } from "vitest";

import { PAGE_GAP, paginationRange } from "../pagination-model.js";

// The arithmetic, tested without rendering. Every awkward case is at an end: a
// window centred on the current page runs off both edges, and a gap of exactly
// one page is worse collapsed than printed.

describe("paginationRange", () => {
	it("lists every page while they fit", () => {
		expect(paginationRange({ page: 1, pageCount: 7 })).toEqual([
			1, 2, 3, 4, 5, 6, 7,
		]);
	});

	it("returns the single page of a one-page collection", () => {
		expect(paginationRange({ page: 1, pageCount: 1 })).toEqual([1]);
	});

	it("returns nothing when there is nothing to page", () => {
		expect(paginationRange({ page: 1, pageCount: 0 })).toEqual([]);
		expect(paginationRange({ page: 1, pageCount: -3 })).toEqual([]);
		expect(paginationRange({ page: 1, pageCount: Number.NaN })).toEqual([]);
	});

	it("collapses only the far end when the page is near the start", () => {
		expect(paginationRange({ page: 2, pageCount: 40 })).toEqual([
			1,
			2,
			3,
			PAGE_GAP,
			40,
		]);
	});

	it("collapses only the near end when the page is near the finish", () => {
		expect(paginationRange({ page: 39, pageCount: 40 })).toEqual([
			1,
			PAGE_GAP,
			38,
			39,
			40,
		]);
	});

	it("collapses both ends in the middle", () => {
		expect(paginationRange({ page: 20, pageCount: 40 })).toEqual([
			1,
			PAGE_GAP,
			19,
			20,
			21,
			PAGE_GAP,
			40,
		]);
	});

	// An ellipsis is the same width as the page it hides and one more click to
	// reach it, so a gap of exactly one page prints the page.
	it("prints a page rather than an ellipsis over a gap of one", () => {
		// Page 2 is the only page between the boundary and the window, so it is
		// printed; the four-page gap on the other side is collapsed as usual.
		expect(paginationRange({ page: 4, pageCount: 9, siblings: 1 })).toEqual([
			1,
			2,
			3,
			4,
			5,
			PAGE_GAP,
			9,
		]);
	});

	it("clamps a page outside the collection instead of trusting it", () => {
		expect(paginationRange({ page: 0, pageCount: 40 })).toEqual(
			paginationRange({ page: 1, pageCount: 40 }),
		);
		expect(paginationRange({ page: 99, pageCount: 40 })).toEqual(
			paginationRange({ page: 40, pageCount: 40 }),
		);
	});

	it("widens and narrows with siblings", () => {
		expect(paginationRange({ page: 20, pageCount: 40, siblings: 0 })).toEqual([
			1,
			PAGE_GAP,
			20,
			PAGE_GAP,
			40,
		]);
		expect(paginationRange({ page: 20, pageCount: 40, siblings: 2 })).toEqual([
			1,
			PAGE_GAP,
			18,
			19,
			20,
			21,
			22,
			PAGE_GAP,
			40,
		]);
	});

	it("never repeats a page or goes backwards", () => {
		for (let pageCount = 1; pageCount <= 30; pageCount += 1) {
			for (let page = 1; page <= pageCount; page += 1) {
				const numbers = paginationRange({ page, pageCount }).filter(
					(entry): entry is number => entry !== PAGE_GAP,
				);
				expect(new Set(numbers).size, `page ${page} of ${pageCount}`).toBe(
					numbers.length,
				);
				expect([...numbers].sort((a, b) => a - b)).toEqual(numbers);
				expect(numbers).toContain(page);
			}
		}
	});
});
