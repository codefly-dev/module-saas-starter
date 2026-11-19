import { describe, expect, it } from "vitest";
import { FIRST_PAGE, nextPage, pageNumber, previousPage } from "./page-cursor";
import { queueTiles, queueTotals } from "./queue-totals";

const queue = (
	ready: number,
	processing: number,
	deadLetter: number,
	expiredLeases: number,
) => ({
	ready: BigInt(ready),
	processing: BigInt(processing),
	deadLetter: BigInt(deadLetter),
	expiredLeases: BigInt(expiredLeases),
});

describe("queueTotals", () => {
	it("sums each count across the queues", () => {
		expect(queueTotals([queue(5, 2, 1, 0), queue(7, 0, 3, 4)])).toEqual({
			ready: 12,
			processing: 2,
			failedForGood: 4,
			stalled: 4,
		});
	});

	it("is all zeros for an answer with no queues", () => {
		expect(queueTotals([])).toEqual({
			ready: 0,
			processing: 0,
			failedForGood: 0,
			stalled: 0,
		});
	});
});

describe("queueTiles", () => {
	it("labels the four counts in plain words", () => {
		expect(queueTiles(queueTotals([queue(5, 2, 1, 3)]))).toEqual([
			{ id: "ready", label: "Waiting to start", value: 5 },
			{ id: "processing", label: "Running now", value: 2 },
			{ id: "failedForGood", label: "Failed for good", value: 1 },
			{ id: "stalled", label: "Stalled mid-run", value: 3 },
		]);
	});

	it("has no numbers, rather than zeros, when there is no answer", () => {
		expect(queueTiles(undefined).map((tile) => tile.value)).toEqual([
			null,
			null,
			null,
			null,
		]);
	});
});

describe("page cursor", () => {
	it("walks forward and back through server page tokens", () => {
		const second = nextPage(FIRST_PAGE, "t2");
		const third = nextPage(second, "t3");
		expect(third).toEqual({ token: "t3", previous: ["", "t2"] });
		expect(pageNumber(third)).toBe(3);
		expect(previousPage(third)).toEqual(second);
		expect(previousPage(second)).toEqual(FIRST_PAGE);
	});

	it("stays put at either end", () => {
		expect(previousPage(FIRST_PAGE)).toBe(FIRST_PAGE);
		expect(nextPage(FIRST_PAGE, "")).toBe(FIRST_PAGE);
		expect(pageNumber(FIRST_PAGE)).toBe(1);
	});
});
