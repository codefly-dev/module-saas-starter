// @vitest-environment happy-dom
import { act } from "react";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";
import { mountIsolated } from "./mount.js";

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: unknown) => void;
	const promise = new Promise<T>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}
const flush = async () => {
	await Promise.resolve();
	await Promise.resolve();
	await Promise.resolve();
};

describe("isolated mounts", () => {
	it("keeps the newer React root when a retired asynchronous setup finishes", async () => {
		const host = document.createElement("div");
		document.body.appendChild(host);
		const gate = deferred<void>();
		const ready = vi.fn();
		const errors = vi.fn();
		const oldDispose = vi.fn();
		const newDispose = vi.fn();
		const children: HTMLElement[] = [];
		const draw = async (
			element: HTMLElement,
			label: string,
			wait: Promise<void>,
			dispose: () => void,
		) => {
			children.push(element);
			await wait;
			const root = createRoot(element);
			flushSync(() => root.render(<button type="button">{label}</button>));
			return {
				dispose: () => {
					dispose();
					root.unmount();
				},
			};
		};
		const old = mountIsolated(
			host,
			(element) => draw(element, "first", gate.promise, oldDispose),
			{ onReady: ready, onError: errors },
		);
		await flush();
		old.retire();
		const latest = mountIsolated(
			host,
			(element) => draw(element, "second", Promise.resolve(), newDispose),
			{ onReady: ready, onError: errors },
		);
		await flush();
		expect(host.textContent).toBe("second");
		expect(children[0]).not.toBe(children[1]);
		expect(children[0].isConnected).toBe(false);
		await act(async () => {
			gate.resolve();
			await flush();
		});
		expect(host.textContent).toBe("second");
		expect(ready).toHaveBeenCalledTimes(1);
		expect(oldDispose).toHaveBeenCalledTimes(1);
		expect(newDispose).not.toHaveBeenCalled();
		latest.retire();
		latest.retire();
		expect(newDispose).toHaveBeenCalledTimes(1);
		expect(errors).not.toHaveBeenCalled();
		expect(host.childElementCount).toBe(0);
		host.remove();
	});
	it("retires just its own child and disposes once", async () => {
		const host = document.createElement("div");
		const sibling = document.createElement("span");
		host.appendChild(sibling);
		const dispose = vi.fn();
		const mounted = mountIsolated(host, async () => ({ dispose }), {
			onReady: vi.fn(),
			onError: vi.fn(),
		});
		await flush();
		mounted.retire();
		mounted.retire();
		expect([...host.children]).toEqual([sibling]);
		expect(dispose).toHaveBeenCalledTimes(1);
	});
	it("reports synchronous and asynchronous setup failures and removes their child", async () => {
		for (const mount of [
			() => {
				throw new Error("sync");
			},
			async () => {
				throw new Error("async");
			},
		]) {
			const host = document.createElement("div");
			const error = vi.fn();
			mountIsolated(host, mount, { onReady: vi.fn(), onError: error });
			await flush();
			expect(error).toHaveBeenCalledWith(expect.any(Error), false);
			expect(host.childElementCount).toBe(0);
		}
	});
	it("reports a retired setup rejection without assigning it active ownership", async () => {
		const host = document.createElement("div");
		const pending = deferred<{ dispose(): void }>();
		const error = vi.fn();
		const mount = mountIsolated(host, () => pending.promise, {
			onReady: vi.fn(),
			onError: error,
		});
		mount.retire();
		pending.reject(new Error("late"));
		await flush();
		expect(error).toHaveBeenCalledWith(
			expect.objectContaining({ message: "late" }),
			true,
		);
		expect(host.childElementCount).toBe(0);
	});
	it("reports failed disposal once, leaving the sibling untouched", async () => {
		const host = document.createElement("div");
		const sibling = document.createElement("div");
		host.appendChild(sibling);
		const error = vi.fn();
		const mount = mountIsolated(
			host,
			async () => ({
				dispose: () => {
					throw new Error("cleanup");
				},
			}),
			{ onReady: vi.fn(), onError: error },
		);
		await flush();
		mount.retire();
		mount.retire();
		expect(error).toHaveBeenCalledTimes(1);
		expect([...host.children]).toEqual([sibling]);
	});
	it("releases resources if the ready consumer throws", async () => {
		const host = document.createElement("div");
		const dispose = vi.fn();
		const error = vi.fn();
		mountIsolated(host, async () => ({ dispose }), {
			onReady: () => {
				throw new Error("consumer");
			},
			onError: error,
		});
		await flush();
		expect(dispose).toHaveBeenCalledTimes(1);
		expect(error).toHaveBeenCalledWith(
			expect.objectContaining({ message: "consumer" }),
			false,
		);
		expect(host.childElementCount).toBe(0);
	});
});

it("aborts pending setup before detaching and still disposes a late handle that ignores cancellation", async () => {
	const host = document.createElement("div");
	const pending = deferred<{ dispose(): void }>();
	const ready = vi.fn();
	const dispose = vi.fn();
	const events: string[] = [];
	let signal!: AbortSignal;
	const mounted = mountIsolated(
		host,
		(element, received) => {
			signal = received;
			signal.addEventListener("abort", () => {
				expect(element.parentElement).toBe(host);
				expect(dispose).not.toHaveBeenCalled();
				events.push("abort");
			});
			return pending.promise;
		},
		{ onReady: ready, onError: vi.fn() },
	);
	await flush();
	expect(signal.aborted).toBe(false);
	mounted.retire();
	mounted.retire();
	expect(signal.aborted).toBe(true);
	expect(events).toEqual(["abort"]);
	expect(host.childElementCount).toBe(0);
	pending.resolve({ dispose });
	await flush();
	expect(dispose).toHaveBeenCalledTimes(1);
	expect(ready).not.toHaveBeenCalled();
});
it("lets synchronous abort teardown run before returned handle disposal", async () => {
	const host = document.createElement("div");
	const events: string[] = [];
	const mounted = mountIsolated(
		host,
		async (element, signal) => {
			signal.addEventListener("abort", () => {
				events.push("partial-cleanup");
				expect(element.parentElement).toBe(host);
			});
			return {
				dispose: () => {
					events.push("dispose");
					expect(element.parentElement).toBeNull();
				},
			};
		},
		{ onReady: vi.fn(), onError: vi.fn() },
	);
	await flush();
	mounted.retire();
	expect(events).toEqual(["partial-cleanup", "dispose"]);
});
it("passes an already-aborted signal when retired before setup starts", async () => {
	const host = document.createElement("div");
	const ready = vi.fn();
	const dispose = vi.fn();
	let aborted = false;
	const mounted = mountIsolated(
		host,
		async (_element, signal) => {
			aborted = signal.aborted;
			return { dispose };
		},
		{ onReady: ready, onError: vi.fn() },
	);
	mounted.retire();
	await flush();
	expect(aborted).toBe(true);
	expect(dispose).toHaveBeenCalledTimes(1);
	expect(ready).not.toHaveBeenCalled();
});
