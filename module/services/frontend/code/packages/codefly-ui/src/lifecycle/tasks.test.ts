import { describe, expect, it } from "vitest";
import { createTaskTracker } from "./tasks.js";

describe("captured tasks", () => {
	it("retains out-of-order outcomes against captured inputs without adopting them", () => {
		const tracker = createTaskTracker<
			{ value: string },
			{ output: string; preview: string }
		>();
		const first = tracker.begin({ value: "first" });
		tracker.invalidate();
		const second = tracker.begin({ value: "second" });
		tracker.settle(second, { output: "second output", preview: "ready" });
		tracker.settle(first, { output: "first output", preview: "refused" });
		expect(tracker.isCurrent(first)).toBe(false);
		expect(tracker.isCurrent(second)).toBe(true);
		expect(tracker.entries({ limit: 2 })).toEqual([
			{
				task: { context: { value: "first" } },
				state: "settled",
				outcome: { output: "first output", preview: "refused" },
			},
			{
				task: { context: { value: "second" } },
				state: "settled",
				outcome: { output: "second output", preview: "ready" },
			},
		]);
	});
	it("invalidates presentation without dropping successful late work", () => {
		const tracker = createTaskTracker<string, number>();
		const task = tracker.begin("first");
		tracker.invalidate();
		tracker.settle(task, 42);
		expect(tracker.isCurrent(task)).toBe(false);
		expect(tracker.entries({ limit: 1 })[0]).toMatchObject({
			state: "settled",
			outcome: 42,
		});
	});
	it("pages retained entries, including empty and exhausted pages", () => {
		const tracker = createTaskTracker<string, number>();
		expect(tracker.entries({ limit: 10 })).toEqual([]);
		const tasks = ["a", "b", "c"].map((value) => tracker.begin(value));
		expect(
			tracker.entries({ offset: 1, limit: 1 }).map((entry) => entry.task),
		).toEqual([tasks[1]]);
		expect(tracker.entries({ limit: 0 })).toEqual([]);
		expect(tracker.entries({ offset: 3, limit: 1 })).toEqual([]);
		for (const page of [
			{ offset: -1, limit: 1 },
			{ limit: Number.NaN },
			{ limit: 0.5 },
			{ limit: Infinity },
		]) {
			expect(() => tracker.entries(page)).toThrow("safe integers");
		}
	});
	it("forgets only settled work and clears current ownership", () => {
		const tracker = createTaskTracker<string, number>();
		const task = tracker.begin("first");
		expect(() => tracker.forget(task)).toThrow("Pending work");
		tracker.settle(task, 1);
		tracker.forget(task);
		expect(tracker.entries({ limit: 1 })).toEqual([]);
		expect(tracker.isCurrent(task)).toBe(false);
		expect(() => tracker.forget(task)).toThrow("does not belong");
	});
	it("refuses foreign tasks and duplicate settlement instead of losing outcomes", () => {
		const tracker = createTaskTracker<string, number>();
		const foreign = createTaskTracker<string, number>().begin("first");
		expect(() => tracker.settle(foreign, 1)).toThrow("does not belong");
		const own = tracker.begin("first");
		tracker.settle(own, 1);
		expect(() => tracker.settle(own, 2)).toThrow("already settled");
		expect(tracker.entries({ limit: 1 })[0]).toMatchObject({ outcome: 1 });
	});
});
