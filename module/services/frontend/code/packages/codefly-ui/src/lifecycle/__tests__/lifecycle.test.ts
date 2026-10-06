// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createTaskTracker, mountIsolated } from "../index.js";

const tick = async () => {
	for (let n = 0; n < 5; n++) await Promise.resolve();
};
afterEach(() => {
	document.body.replaceChildren();
});
function host() {
	const element = document.createElement("div");
	document.body.append(element);
	return element;
}

describe("mountIsolated", () => {
	it("retires an old pending mount without clearing its replacement", async () => {
		const element = host(),
			dispose = vi.fn(),
			ready = vi.fn(),
			error = vi.fn();
		let finish!: (value: { dispose(): void }) => void;
		let signal!: AbortSignal;
		const first = mountIsolated(
			element,
			async (_, ownedSignal) => {
				signal = ownedSignal;
				return new Promise((resolve) => {
					finish = resolve;
				});
			},
			{ onReady: ready, onError: error },
		);
		await tick();
		first.retire();
		const second = mountIsolated(
			element,
			async (child) => {
				child.textContent = "Current";
				return { dispose: vi.fn() };
			},
			{ onReady: ready, onError: error },
		);
		finish({ dispose });
		await tick();
		expect(signal.aborted).toBe(true);
		expect(dispose).toHaveBeenCalledOnce();
		expect(element.textContent).toBe("Current");
		expect(element.children).toHaveLength(1);
		expect(ready).toHaveBeenCalledOnce();
		first.retire();
		expect(dispose).toHaveBeenCalledOnce();
		second.retire();
	});
	it("routes synchronous setup failure through the error path and removes only its child", async () => {
		const element = host(),
			error = vi.fn(),
			failure = new Error("setup");
		mountIsolated(
			element,
			() => {
				throw failure;
			},
			{ onReady: vi.fn(), onError: error },
		);
		const sibling = document.createElement("aside");
		element.append(sibling);
		await tick();
		expect(error).toHaveBeenCalledWith(failure, false);
		expect(element.children).toHaveLength(1);
		expect(element.firstChild).toBe(sibling);
	});
	it("disposes a ready mount exactly once, including after a ready callback fails", async () => {
		const element = host(),
			dispose = vi.fn(),
			error = vi.fn(),
			failure = new Error("ready");
		const mount = mountIsolated(element, async () => ({ dispose }), {
			onReady: () => {
				throw failure;
			},
			onError: error,
		});
		await tick();
		mount.retire();
		expect(dispose).toHaveBeenCalledOnce();
		expect(element.children).toHaveLength(0);
		expect(error).toHaveBeenCalledWith(failure, false);
	});
	it("marks late setup rejection as retired", async () => {
		const error = vi.fn();
		let reject!: (reason: unknown) => void;
		const mount = mountIsolated(
			host(),
			() =>
				new Promise<never>((_, fail) => {
					reject = fail;
				}),
			{ onReady: vi.fn(), onError: error },
		);
		await tick();
		mount.retire();
		reject("retired setup");
		await tick();
		expect(error).toHaveBeenCalledWith("retired setup", true);
	});
});

describe("captured task outcomes", () => {
	it("retains an old outcome when presentation moves to a new task or is invalidated", () => {
		const tracker = createTaskTracker<string, string>();
		const old = tracker.begin("old"),
			current = tracker.begin("current");
		expect(tracker.isCurrent(old)).toBe(false);
		tracker.settle(old, "old result");
		tracker.invalidate();
		tracker.settle(current, "current result");
		expect(
			tracker
				.entries({ limit: 2 })
				.map((entry) => entry.state === "settled" && entry.outcome),
		).toEqual(["old result", "current result"]);
		tracker.forget(old);
		expect(tracker.entries({ limit: 10 })).toHaveLength(1);
	});
	it("rejects foreign tasks, duplicate completion and forgetting pending work", () => {
		const tracker = createTaskTracker<string, string>(),
			foreign = createTaskTracker<string, string>().begin("foreign"),
			task = tracker.begin("owned");
		expect(() => tracker.settle(foreign, "result")).toThrow(/belong/);
		expect(() => tracker.forget(task)).toThrow(/Pending/);
		tracker.settle(task, "result");
		expect(() => tracker.settle(task, "again")).toThrow(/already settled/);
	});
	it("bounds enumeration without losing retained ownership", () => {
		const tracker = createTaskTracker<number, string>();
		for (let n = 0; n < 4; n++) tracker.begin(n);
		expect(
			tracker
				.entries({ offset: 1, limit: 2 })
				.map((entry) => entry.task.context),
		).toEqual([1, 2]);
		expect(tracker.entries({ limit: 0 })).toEqual([]);
		expect(() => tracker.entries({ limit: -1 })).toThrow();
		expect(() => tracker.entries({ offset: 0.5, limit: 1 })).toThrow();
	});
});
